package fs

import "testing"

func TestNamedCopyStageObjectPath(t *testing.T) {
	tests := []struct {
		name     string
		stageDir string
		srcPath  string
		dstName  string
		want     string
	}{
		{name: "source basename", stageDir: "/dst/.stage", srcPath: "/src/foo", want: "/dst/.stage/foo"},
		{name: "requested name", stageDir: "/dst/.stage", srcPath: "/src/foo", dstName: "bar", want: "/dst/.stage/bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := namedCopyStageObjectPath(tt.stageDir, tt.srcPath, tt.dstName); got != tt.want {
				t.Fatalf("namedCopyStageObjectPath(%q, %q, %q) = %q, want %q", tt.stageDir, tt.srcPath, tt.dstName, got, tt.want)
			}
		})
	}
}
