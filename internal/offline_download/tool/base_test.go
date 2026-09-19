package tool

import "testing"

type testCapabilityProvider struct {
	Tool
	capabilities Capabilities
}

func (t testCapabilityProvider) Capabilities() Capabilities {
	return t.capabilities
}

func TestCapabilitiesOf(t *testing.T) {
	tests := []struct {
		name         string
		downloadTool Tool
		want         Capabilities
	}{
		{
			name:         "tool without provider",
			downloadTool: struct{ Tool }{},
			want:         Capabilities{},
		},
		{
			name: "tool with torrent data support",
			downloadTool: testCapabilityProvider{
				capabilities: Capabilities{TorrentData: true},
			},
			want: Capabilities{TorrentData: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CapabilitiesOf(tt.downloadTool); got != tt.want {
				t.Fatalf("CapabilitiesOf() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
