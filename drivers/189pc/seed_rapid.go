package _189pc

import (
	"context"
	"errors"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// RapidHashAlgos 返回 189pc 支持的秒传哈希算法（MD5）
func (d *Cloud189PC) RapidHashAlgos() []utils.HashType {
	return []utils.HashType{*utils.MD5}
}

// RapidHashNeedsPieces 189pc 的 CAS 秒传依赖分片 MD5
func (d *Cloud189PC) RapidHashNeedsPieces() bool {
	return true
}

// RapidUploadByHashes 使用种子中的哈希尝试秒传。
//
// 天翼云盘的秒传要经过 initMultiUpload → checkTransSecond → commitMultiUploadFile，
// 且除整文件 MD5 外还要用 sliceMd5 判重：大于单个分片的文件只带整文件 MD5
// 是匹配不上的。种子里既然带了分片 MD5（或旧 .cas 载荷里的 sliceMd5），就
// 直接走 CAS；只有整文件 MD5 时才退回旧上传接口。
func (d *Cloud189PC) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	md5Hash := req.Whole.GetHash(utils.MD5)
	if len(md5Hash) < utils.MD5.Width {
		return nil, errs.ErrUnavailableHash
	}

	var casErr error
	if req.SliceMD5 != "" || len(req.SliceMD5s) > 0 {
		// 只有旧载荷的现成摘要时，种子里没有可信的分片大小，
		// 交给驱动按文件大小推导（天翼的分片大小随文件大小变化）
		sliceSize := req.SliceSize
		if len(req.SliceMD5s) == 0 {
			sliceSize = 0
		}
		obj, err := d.rapidUploadByCAS(ctx, dstDir, req.Name, req.Size, md5Hash, req.SliceMD5, req.SliceMD5s, sliceSize, overwrite)
		if err == nil {
			return obj, nil
		}
		casErr = err
		// 当日流量用尽是账号级状态：旧接口同样是「建上传会话」，
		// 再试一次只会再报同样的错
		if isDayFlowOverLimited(err) {
			return nil, wrapDayFlowOverLimit(err)
		}
	}

	stream := driver.NewSeedHashStream(req)
	obj, err := d.RapidUpload(ctx, dstDir, stream, d.isFamily(), overwrite)
	if err != nil {
		// CAS 的报错更具体（云端不存在 / 分片不匹配），失败时优先上报它
		if casErr != nil {
			return nil, wrapRapidUploadError(casErr)
		}
		return nil, wrapRapidUploadError(err)
	}
	return obj, nil
}

// wrapRapidUploadError 把账号级限流翻译成可判断、可读的错误
func wrapRapidUploadError(err error) error {
	if isDayFlowOverLimited(err) {
		return wrapDayFlowOverLimit(err)
	}
	return err
}

func wrapDayFlowOverLimit(err error) error {
	return errs.NewErr(errs.DailyFlowLimitExceeded,
		"天翼云盘账号当日上传流量已用尽，需等次日重置或更换账号/开通会员（%v）", err)
}

// isDayFlowOverLimited reports whether the drive refused the request because the
// account's daily upload/transfer flow is exhausted (UserDayFlowOverLimited).
// 天翼把这类业务错误放在 HTTP 200 的响应体里，只能按错误码文本判断。
func isDayFlowOverLimited(err error) bool {
	var respErr *RespErr
	if !errors.As(err, &respErr) {
		return false
	}
	text := strings.ToLower(strings.Join([]string{
		respErr.Code, respErr.Msg, respErr.Message, respErr.ErrorMsg, respErr.ResMessage,
	}, " "))
	return strings.Contains(text, "userdayflowoverlimited") || strings.Contains(text, "flow is out")
}
