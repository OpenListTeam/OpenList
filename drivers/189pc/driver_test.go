package _189pc

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
)

func TestFamilyTransferTempNamePreservesExtension(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "photo.jpg", want: ".jpg"},
		{name: "PHOTO.PNG", want: ".PNG"},
		{name: "archive.tar.gz", want: ".gz"},
		{name: "no-extension", want: ".transfer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := familyTransferTempName(tt.name)
			if !strings.HasPrefix(got, "0") {
				t.Fatalf("temporary name %q must start with 0", got)
			}
			if ext := path.Ext(got); ext != tt.want {
				t.Fatalf("temporary name extension = %q, want %q", ext, tt.want)
			}
		})
	}
}

// TestCASSliceDigestPrefersFinalDigest 最终摘要优先：旧 .cas 只存了这个值，
// 它已经是逐片 MD5 的汇总结果，不能因为同时存在逐片列表就重新推算覆盖它。
func TestCASSliceDigestPrefersFinalDigest(t *testing.T) {
	fileMD5 := strings.Repeat("1", 32)
	parts := []string{strings.Repeat("a", 32), strings.Repeat("b", 32)}

	if got, want := casSliceDigest(fileMD5, "", parts), torrent.SliceMD5FromPieces(parts, fileMD5); got != want {
		t.Fatalf("casSliceDigest() without explicit digest = %q, want %q", got, want)
	}
	explicit := strings.Repeat("F", 32)
	if got := casSliceDigest(fileMD5, strings.ToLower(explicit), parts); got != explicit {
		t.Fatalf("casSliceDigest() = %q, want the explicit digest %q", got, explicit)
	}
	if got := casSliceDigest(fileMD5, "", nil); got != strings.ToUpper(fileMD5) {
		t.Fatalf("casSliceDigest() without any slice info = %q, want file MD5", got)
	}
}

// TestIsDayFlowOverLimited 天翼把当日流量用尽作为 HTTP 200 的业务错误码返回，
// 只能按错误码文本识别；识别不出来就只能在批量秒传里逐个文件重复失败。
func TestIsDayFlowOverLimited(t *testing.T) {
	quota := &RespErr{Code: "UserDayFlowOverLimited", Msg: "sessionKey=abc,data flow is out"}
	if !isDayFlowOverLimited(fmt.Errorf("initMultiUpload failed: %w", quota)) {
		t.Fatal("UserDayFlowOverLimited was not recognized through the error chain")
	}
	if isDayFlowOverLimited(&RespErr{Code: "InvalidSessionKey"}) {
		t.Fatal("an unrelated business error must not be treated as a quota error")
	}
	if isDayFlowOverLimited(nil) {
		t.Fatal("nil error must not be treated as a quota error")
	}
	if !errors.Is(wrapDayFlowOverLimit(quota), errs.DailyFlowLimitExceeded) {
		t.Fatal("quota errors must be detectable with errors.Is")
	}
}

func TestUploadProgressKeysIncludeExtension(t *testing.T) {
	jpgKeys := uploadProgressKeys("session", "same-md5", "photo.jpg")
	txtKeys := uploadProgressKeys("session", "same-md5", "photo.txt")
	retryKeys := uploadProgressKeys("session", "same-md5", "different-name.jpg")

	if slices.Equal(jpgKeys, txtKeys) {
		t.Fatal("files with different extensions must not share upload progress")
	}
	if !slices.Equal(jpgKeys, retryKeys) {
		t.Fatal("files with the same session, MD5, and extension should share upload progress")
	}
}
