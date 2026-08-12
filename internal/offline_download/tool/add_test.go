package tool

import "testing"

func TestIsEd2kCapableTool(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "115 Cloud", want: true},
		{name: "115 Open", want: true},
		{name: "Thunder", want: true},
		{name: "ThunderX", want: true},
		{name: "ThunderBrowser", want: true},
		{name: "aria2", want: false},
		{name: "SimpleHttp", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEd2kCapableTool(tt.name); got != tt.want {
				t.Fatalf("isEd2kCapableTool(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
