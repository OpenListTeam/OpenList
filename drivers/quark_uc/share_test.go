package quark

import "testing"

func TestParseShareLink(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantID       string
		wantPasscode string
		wantErr      bool
	}{
		{
			name:   "quark url",
			raw:    "https://pan.quark.cn/s/abc123def456",
			wantID: "abc123def456",
		},
		{
			name:   "quark url with hash",
			raw:    "https://pan.quark.cn/s/abc123def456#/list/share",
			wantID: "abc123def456",
		},
		{
			name:         "quark url with query pwd",
			raw:          "https://pan.quark.cn/s/abc123def456?pwd=xy12",
			wantID:       "abc123def456",
			wantPasscode: "xy12",
		},
		{
			name:         "quark url with chinese passcode",
			raw:          "链接: https://pan.quark.cn/s/abc123def456 提取码: abcd",
			wantID:       "abc123def456",
			wantPasscode: "abcd",
		},
		{
			name:   "uc url",
			raw:    "https://drive.uc.cn/s/ucshareid01",
			wantID: "ucshareid01",
		},
		{
			name:    "unsupported",
			raw:     "https://pan.baidu.com/s/xxxx",
			wantErr: true,
		},
		{
			name:    "empty",
			raw:     "   ",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotPass, err := ParseShareLink(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got pwdID=%s", gotID)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotID != tt.wantID {
				t.Fatalf("pwdID = %s, want %s", gotID, tt.wantID)
			}
			if gotPass != tt.wantPasscode {
				t.Fatalf("passcode = %s, want %s", gotPass, tt.wantPasscode)
			}
		})
	}
}
