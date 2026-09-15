package streamtape

import (
	"errors"
	"testing"
)

func TestExtractWaitSecondsFromErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "nil error",
			err:  nil,
			want: 0,
		},
		{
			name: "plural seconds",
			err:  errors.New("streamtape api error: please wait 15 more seconds before retrying"),
			want: 15,
		},
		{
			name: "singular second",
			err:  errors.New("Wait 1 more second"),
			want: 1,
		},
		{
			name: "unrelated error",
			err:  errors.New("network connection timeout"),
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractWaitSecondsFromErr(tc.err)
			if got != tc.want {
				t.Errorf("extractWaitSecondsFromErr() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestIDEncodingDecoding(t *testing.T) {
	// Folder ID encoding
	if got := encodeFolderID(""); got != "d:0" {
		t.Errorf("encodeFolderID(\"\") = %q, want %q", got, "d:0")
	}
	if got := encodeFolderID("0"); got != "d:0" {
		t.Errorf("encodeFolderID(\"0\") = %q, want %q", got, "d:0")
	}
	if got := encodeFolderID("/"); got != "d:0" {
		t.Errorf("encodeFolderID(\"/\") = %q, want %q", got, "d:0")
	}
	if got := encodeFolderID("folder123"); got != "d:folder123" {
		t.Errorf("encodeFolderID(\"folder123\") = %q, want %q", got, "d:folder123")
	}

	// Folder ID decoding
	if got := folderIDFromObjID(""); got != "0" {
		t.Errorf("folderIDFromObjID(\"\") = %q, want %q", got, "0")
	}
	if got := folderIDFromObjID("d:folder123"); got != "folder123" {
		t.Errorf("folderIDFromObjID(\"d:folder123\") = %q, want %q", got, "folder123")
	}
	if got := folderIDFromObjID("folder123"); got != "folder123" {
		t.Errorf("folderIDFromObjID(\"folder123\") = %q, want %q", got, "folder123")
	}

	// File ID encoding
	if got := encodeFileID(""); got != "" {
		t.Errorf("encodeFileID(\"\") = %q, want %q", got, "")
	}
	if got := encodeFileID("file123"); got != "f:file123" {
		t.Errorf("encodeFileID(\"file123\") = %q, want %q", got, "f:file123")
	}
	if got := encodeFileID("f:file123"); got != "f:file123" {
		t.Errorf("encodeFileID(\"f:file123\") = %q, want %q", got, "f:file123")
	}

	// File ID decoding
	if got := fileIDFromObjID("f:file123"); got != "file123" {
		t.Errorf("fileIDFromObjID(\"f:file123\") = %q, want %q", got, "file123")
	}
	if got := fileIDFromObjID("file123"); got != "file123" {
		t.Errorf("fileIDFromObjID(\"file123\") = %q, want %q", got, "file123")
	}

	// Remote Upload ID
	if got := encodeRemoteUploadID("ru123"); got != "ru:ru123" {
		t.Errorf("encodeRemoteUploadID(\"ru123\") = %q, want %q", got, "ru:ru123")
	}
	if got := remoteUploadIDFromObjID("ru:ru123"); got != "ru123" {
		t.Errorf("remoteUploadIDFromObjID(\"ru:ru123\") = %q, want %q", got, "ru123")
	}
	if got := remoteUploadIDFromObjID("file123"); got != "" {
		t.Errorf("remoteUploadIDFromObjID(\"file123\") = %q, want %q", got, "")
	}
	if !isRemoteUploadID("ru:ru123") {
		t.Errorf("isRemoteUploadID(\"ru:ru123\") = false, want true")
	}
	if isRemoteUploadID("file123") {
		t.Errorf("isRemoteUploadID(\"file123\") = true, want false")
	}
}

func TestExtractFileIDFromUploadBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "empty body",
			body: "",
			want: "",
		},
		{
			name: "invalid json",
			body: "{not-valid-json",
			want: "",
		},
		{
			name: "status not 200",
			body: `{"status":400,"msg":"error","result":null}`,
			want: "",
		},
		{
			name: "result with file key",
			body: `{"status":200,"msg":"OK","result":{"file":"abc123xyz"}}`,
			want: "abc123xyz",
		},
		{
			name: "result with id key",
			body: `{"status":200,"msg":"OK","result":{"id":"id12345"}}`,
			want: "id12345",
		},
		{
			name: "result with fileid key",
			body: `{"status":200,"msg":"OK","result":{"fileid":"fid6789"}}`,
			want: "fid6789",
		},
		{
			name: "result with linkid key",
			body: `{"status":200,"msg":"OK","result":{"linkid":"lid9999"}}`,
			want: "lid9999",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFileIDFromUploadBody([]byte(tc.body))
			if got != tc.want {
				t.Errorf("extractFileIDFromUploadBody() = %q, want %q", got, tc.want)
			}
		})
	}
}
