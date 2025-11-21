package plugin_warp

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	hash_extend "github.com/OpenListTeam/OpenList/v4/pkg/utils/hash"
	witgo "github.com/OpenListTeam/wazero-wasip2/wit-go"
)

type UploadRequest struct {
	Target Object
	// 指向宿主端文件内容的句柄
	// 由host控制释放
	Content UploadReadable
	// 如果是覆盖上传，宿主会提供被覆盖文件的原始对象数据
	Exist witgo.Option[Object]
}

type UploadReadableType struct {
	model.FileStreamer
	StreamConsume  bool
	UpdateProgress driver.UpdateProgress
	SectionReader  *StreamSectionReader
}

type StreamSectionReader struct {
	stream.StreamSectionReaderIF
	Offset     int64
	CunketSize int64
}

type UploadReadableManager = witgo.ResourceManager[*UploadReadableType]
type UploadReadable = uint32

func NewUploadManager() *UploadReadableManager {
	return witgo.NewResourceManager[*UploadReadableType](nil)
}

func HashTypeConvert(typ *utils.HashType) HashAlg {
	switch typ {
	case utils.MD5:
		return HashAlg{MD5: &struct{}{}}
	case utils.SHA1:
		return HashAlg{SHA1: &struct{}{}}
	case utils.SHA256:
		return HashAlg{SHA256: &struct{}{}}
	case hash_extend.GCID:
		return HashAlg{GCID: &struct{}{}}
	}
	panic("plase add hash convert")
}
func HashAlgConvert(hash HashAlg) *utils.HashType {
	if hash.MD5 != nil {
		return utils.MD5
	} else if hash.SHA1 != nil {
		return utils.SHA1
	} else if hash.SHA256 != nil {
		return utils.SHA256
	} else if hash.GCID != nil {
		return hash_extend.GCID
	}
	panic("plase add hash convert")
}
func HashAlgConverts(HashAlgs []HashAlg) []*utils.HashType {
	hashTypes := make([]*utils.HashType, 0, len(HashAlgs))
	for _, needHash := range HashAlgs {
		hashTypes = append(hashTypes, HashAlgConvert(needHash))
	}
	return hashTypes
}

func HashInfoConvert(hashInfo utils.HashInfo) []HashInfo {
	result := make([]HashInfo, 0, 4)
	for hash, val := range hashInfo.All() {
		if hash.Width != len(val) {
			continue
		}
		result = append(result, HashInfo{Alg: HashTypeConvert(hash), Val: val})
	}

	return result
}

func HashInfoConvert2(hashInfo utils.HashInfo, needHashs []HashAlg) []HashInfo {
	resultHashs := make([]HashInfo, 0, len(needHashs))

	for _, needHash := range needHashs {
		hashType := HashAlgConvert(needHash)
		hash := hashInfo.GetHash(hashType)
		if hashType.Width != len(hash) {
			return nil
		}
		resultHashs = append(resultHashs, HashInfo{Alg: needHash, Val: hash})
	}
	return resultHashs
}
func HashInfoConvert3(hashInfo []HashInfo) utils.HashInfo {
	newHashInfo := make(map[*utils.HashType]string, len(hashInfo))
	for _, hashInfo := range hashInfo {
		newHashInfo[HashAlgConvert(hashInfo.Alg)] = hashInfo.Val
	}
	return utils.NewHashInfoByMap(newHashInfo)
}
