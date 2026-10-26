package handles

import (
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

const (
	// 单个条目最多保存的批注数，防止恶意请求把表撑大
	maxMediaAnnotations = 500
	// 单条批注对象（含绘制点集）的 JSON 上限
	maxMediaAnnotationAnchor = 512 << 10
	// 单次请求的批注对象总量上限，避免一次塞进几百个大体积批注
	maxMediaAnnotationTotal = 8 << 20
)

// mediaAnnotationTarget 校验条目存在且当前用户可以写入批注。
// 批注是用户私有数据，不允许访客：访客是公共账号，多设备之间的批注会互相覆盖。
func mediaAnnotationTarget(c *gin.Context, itemID uint) (*model.MediaItem, *model.User, bool) {
	user := mediaUser(c)
	if user == nil {
		common.ErrorStrResp(c, "unauthorized", 401)
		return nil, nil, false
	}
	if user.IsGuest() {
		common.ErrorStrResp(c, "guest cannot save annotations", 403)
		return nil, nil, false
	}
	if itemID == 0 {
		common.ErrorStrResp(c, "invalid request", 400)
		return nil, nil, false
	}
	item, err := db.GetMediaItemByID(itemID)
	if err != nil || item.Hidden || item.DeletedAt.Valid || !CanUserAccessItem(user, item) {
		common.ErrorStrResp(c, "not found", 404)
		return nil, nil, false
	}
	return item, user, true
}

// ListMediaAnnotationsAPI 列出当前用户对某个条目保存的批注
// GET /fs/media/annotations?item_id=1
func ListMediaAnnotationsAPI(c *gin.Context) {
	user := mediaUser(c)
	itemID := uint(atoiDefault(c.Query("item_id"), 0))
	// 未登录 / 访客 / 无权访问一律返回空列表：这是私有数据，不对访客暴露
	if user == nil || user.IsGuest() || itemID == 0 {
		common.SuccessResp(c, []model.MediaAnnotation{})
		return
	}
	item, err := db.GetMediaItemByID(itemID)
	if err != nil || item.Hidden || item.DeletedAt.Valid || !CanUserAccessItem(user, item) {
		common.SuccessResp(c, []model.MediaAnnotation{})
		return
	}
	list, err := db.ListMediaAnnotations(user.ID, item.ID)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, list)
}

type mediaAnnotationPayload struct {
	RemoteID string `json:"remote_id"`
	Anchor   string `json:"anchor"`
}

type mediaAnnotationSyncReq struct {
	ItemID  uint                     `json:"item_id"`
	Upserts []mediaAnnotationPayload `json:"upserts"`
	Deletes []string                 `json:"deletes"`
}

// SyncMediaAnnotationsAPI 增量保存批注：upserts 覆盖同一 remote_id，deletes 按 remote_id 删除。
// 查看器每次改动只会带上变化的那几条，不需要整份文档重传。
// POST /fs/media/annotations
func SyncMediaAnnotationsAPI(c *gin.Context) {
	var req mediaAnnotationSyncReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorStrResp(c, "invalid request", 400)
		return
	}
	item, user, ok := mediaAnnotationTarget(c, req.ItemID)
	if !ok {
		return
	}
	if len(req.Upserts) > maxMediaAnnotations || len(req.Deletes) > maxMediaAnnotations {
		common.ErrorStrResp(c, "too many annotations", 400)
		return
	}

	upserts := make([]model.MediaAnnotation, 0, len(req.Upserts))
	seen := make(map[string]struct{}, len(req.Upserts))
	anchorBytes := 0
	for _, p := range req.Upserts {
		remoteID := strings.TrimSpace(p.RemoteID)
		// remote_id 是查看器分配的批注主键，多设备同步时靠它去重，必须存在且唯一
		if remoteID == "" || p.Anchor == "" || len(remoteID) > 128 {
			common.ErrorStrResp(c, "invalid annotation", 400)
			return
		}
		if _, dup := seen[remoteID]; dup {
			continue
		}
		seen[remoteID] = struct{}{}
		if len(p.Anchor) > maxMediaAnnotationAnchor {
			common.ErrorStrResp(c, "annotation too large", 400)
			return
		}
		anchorBytes += len(p.Anchor)
		if anchorBytes > maxMediaAnnotationTotal {
			common.ErrorStrResp(c, "annotations too large", 400)
			return
		}
		upserts = append(upserts, model.MediaAnnotation{
			RemoteID: remoteID,
			Anchor:   p.Anchor,
		})
	}

	deletes := make([]string, 0, len(req.Deletes))
	for _, id := range req.Deletes {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 128 {
			continue
		}
		deletes = append(deletes, id)
	}

	if err := db.SyncMediaAnnotations(user.ID, item.ID, upserts, deletes); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, gin.H{"saved": len(upserts), "deleted": len(deletes)})
}
