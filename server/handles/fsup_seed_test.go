package handles

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/gin-gonic/gin"
)

func seedHeaderContext(t *testing.T, headers map[string]string) *gin.Context {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/fs/put", nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c
}

// TestUploadSeedSliceSizeFollowsCASRule 上传时算哈希用的分片大小必须与 CAS 规则一致：
// 流式哈希出的逐片 MD5 是按这个值切的，sidecar 又按 CASSliceSize 声明分片大小，
// 两者不一致就会出现「片数对不上、.cas 生成失败」。
func TestUploadSeedSliceSizeFollowsCASRule(t *testing.T) {
	const tenMiB = 10 * 1024 * 1024
	c := seedHeaderContext(t, map[string]string{
		"X-Seed-Sidecars":   "oss,cas",
		"X-Seed-Piece-Size": "4194304",
	})
	for _, size := range []int64{3 << 30, tenMiB * 999, tenMiB*2*999 + 1} {
		if got, want := uploadSeedSliceSize(c, size), torrent.CASSliceSize(size); got != want {
			t.Fatalf("uploadSeedSliceSize(%d) = %d, want %d", size, got, want)
		}
	}

	// 不含 cas 时沿用请求头里的 piece size
	plain := seedHeaderContext(t, map[string]string{
		"X-Seed-Sidecars":   "oss",
		"X-Seed-Piece-Size": "4194304",
	})
	if got := uploadSeedSliceSize(plain, 3<<30); got != 4*1024*1024 {
		t.Fatalf("uploadSeedSliceSize() without cas = %d, want the header value", got)
	}
}

// TestSeedSidecarWantsCAS 头解析要能识别大小写与空格：FsStream 依赖它在
// 文件大小未知时提前拒绝 cas sidecar，避免主文件写完才失败。
func TestSeedSidecarWantsCAS(t *testing.T) {
	for formats, want := range map[string]bool{
		"cas":         true,
		"oss,cas":     true,
		" OSS , CAS ": true,
		"oss":         false,
		"torrent,oss": false,
	} {
		c := seedHeaderContext(t, map[string]string{"X-Seed-Sidecars": formats})
		if got := seedSidecarWantsCAS(c); got != want {
			t.Fatalf("seedSidecarWantsCAS(%q) = %v, want %v", formats, got, want)
		}
	}
}
