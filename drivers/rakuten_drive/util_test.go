package rakuten_drive

import (
	"encoding/base64"
	"strconv"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

func makeJWT(payload string, pad bool) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	var body string
	if pad {
		body = base64.URLEncoding.EncodeToString([]byte(payload))
	} else {
		body = base64.RawURLEncoding.EncodeToString([]byte(payload))
	}
	return header + "." + body + ".signature"
}

func TestRemotePath2LocalPath(t *testing.T) {
	d := &RakutenDrive{Addition: Addition{RootPath: driver.RootPath{RootFolderPath: "docs"}}}
	cases := []struct {
		remote string
		want   string
	}{
		{"", "/"},
		{"docs", "/"},
		{"docs/", "/"},
		{"docs/a.txt", "/a.txt"},
		{"docs/sub/a.txt", "/sub/a.txt"},
		{"docs-archive", "/docs-archive"},
		{"other/a.txt", "/other/a.txt"},
	}
	for _, c := range cases {
		if got := d.remotePath2LocalPath(c.remote); got != c.want {
			t.Errorf("remotePath2LocalPath(%q) = %q, want %q", c.remote, got, c.want)
		}
	}
}

func TestParseList(t *testing.T) {
	d := &RakutenDrive{Addition: Addition{RootPath: driver.RootPath{RootFolderPath: ""}}}

	// "data" holding an object with a nested "files" array must not be
	// mistaken for the entry list itself
	body := []byte(`{"data":{"files":[{"path":"a.txt","size":12},{"path":"b.txt","size":0,"is_folder":true}],"total":2}}`)
	objs, raw, err := d.parseList(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw != 2 || len(objs) != 2 {
		t.Fatalf("expected raw=2 objs=2, got raw=%d objs=%d", raw, len(objs))
	}
	if objs[0].GetName() != "a.txt" || objs[0].GetSize() != 12 || objs[0].IsDir() {
		t.Errorf("unexpected object: %+v", objs[0])
	}
	if !objs[1].IsDir() {
		t.Errorf("expected folder: %+v", objs[1])
	}

	// flat "file" array is still matched and joined with the list directory
	body2 := []byte(`{"file":[{"path":"x.txt","size":1}]}`)
	objs2, raw2, err := d.parseList(body2, "sub")
	if err != nil || raw2 != 1 || len(objs2) != 1 {
		t.Fatalf("flat parse failed: objs=%+v raw=%d err=%v", objs2, raw2, err)
	}
	if objs2[0].GetID() != "sub/x.txt" {
		t.Errorf("expected ID sub/x.txt, got %q", objs2[0].GetID())
	}

	// a response without any list field yields an empty result
	objs3, raw3, err := d.parseList([]byte(`{"error":"none"}`), "")
	if err != nil || raw3 != 0 || len(objs3) != 0 {
		t.Fatalf("empty parse failed: objs=%+v raw=%d err=%v", objs3, raw3, err)
	}
}

func TestParseJWTExp(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"valid", makeJWT(`{"exp":`+strconv.FormatInt(exp, 10)+`}`, false), false},
		{"padded payload", makeJWT(`{"exp":`+strconv.FormatInt(exp, 10)+`}`, true), false},
		{"missing exp", makeJWT(`{"sub":"user"}`, false), true},
		{"no dot", "not-a-jwt", true},
		{"invalid base64", "header.!!!.signature", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseJWTExp(c.token)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(time.Unix(exp, 0)) {
				t.Fatalf("expected %v, got %v", time.Unix(exp, 0), got)
			}
		})
	}
}
