package _189pc

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
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

// TestIsDayFlowOverLimited 天翼把当日流量用尽作为 HTTP 200 的业务错误码返回，
// 只能按错误码文本识别；识别不出来就只能在批量秒传里逐个文件重复失败。
func TestIsDayFlowOverLimited(t *testing.T) {
	quota := &RespErr{Code: "UserDayFlowOverLimited", Msg: "sessionKey=abc,data flow is out"}
	if !isDayFlowOverLimited(fmt.Errorf("initMultiUpload 失败: %w", quota)) {
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
