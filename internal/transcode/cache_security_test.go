package transcode

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidProfileName(t *testing.T) {
	ok := []string{"source", "1080p", "720p", "a_b-c", "A1"}
	for _, p := range ok {
		if !ValidProfileName(p) {
			t.Errorf("%q should be valid", p)
		}
	}
	bad := []string{"", "..", "../x", "a/b", `a\b`, "a b", "a.b", strings.Repeat("a", 33), "%2e%2e"}
	for _, p := range bad {
		if ValidProfileName(p) {
			t.Errorf("%q should be rejected", p)
		}
	}
}

func TestPutSegmentRejectsPathTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	c := NewCache(root, 1)
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	_, err := c.PutSegment("job_x", "../../../outside", 0, 1, bytes.NewReader([]byte("x")), false)
	if !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("want ErrInvalidProfile, got %v", err)
	}
	// 目录之外不应出现任何文件
	parent := filepath.Dir(root)
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if e.Name() != "cache" {
			t.Fatalf("unexpected path created outside cache root: %s", e.Name())
		}
	}
	if _, err := c.PutSegment("../job", "source", 0, 1, bytes.NewReader([]byte("x")), false); !errors.Is(err, ErrInvalidJobID) {
		t.Fatalf("want ErrInvalidJobID, got %v", err)
	}
}

func TestPutSegmentLimits(t *testing.T) {
	c := NewCache(filepath.Join(t.TempDir(), "cache"), 1)
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutSegment("job_x", "source", -1, 1, bytes.NewReader([]byte("x")), false); !errors.Is(err, ErrInvalidSegment) {
		t.Fatalf("negative seq: want ErrInvalidSegment, got %v", err)
	}
	big := bytes.NewReader(make([]byte, MaxSegmentBytes+10))
	if _, err := c.PutSegment("job_x", "source", 0, 1, big, false); !errors.Is(err, ErrSegmentTooLarge) {
		t.Fatalf("oversize: want ErrSegmentTooLarge, got %v", err)
	}
	// 超限后不应残留 tmp 文件
	matches, _ := filepath.Glob(filepath.Join(c.rootDir, "job_x", "source", "*"))
	if len(matches) != 0 {
		t.Fatalf("leftover files after rejected upload: %v", matches)
	}
	if _, err := c.PutSegment("job_x", "source", 0, 1, bytes.NewReader([]byte("ok")), false); err != nil {
		t.Fatalf("normal upload failed: %v", err)
	}
}
