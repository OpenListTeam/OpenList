package fs

import "testing"

func TestCanUseNativeCopy(t *testing.T) {
	tests := []struct {
		name        string
		sameStorage bool
		dstName     string
		want        bool
	}{
		{name: "unnamed same-storage copy", sameStorage: true, want: true},
		{name: "named same-storage copy", sameStorage: true, dstName: "bar", want: false},
		{name: "unnamed cross-storage copy", sameStorage: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canUseNativeCopy(tt.sameStorage, tt.dstName); got != tt.want {
				t.Fatalf("canUseNativeCopy(%t, %q) = %t, want %t", tt.sameStorage, tt.dstName, got, tt.want)
			}
		})
	}
}
