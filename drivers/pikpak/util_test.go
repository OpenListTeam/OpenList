package pikpak

import (
	"testing"
)

func TestGetAction(t *testing.T) {
	tests := []struct {
		method string
		url    string
		want   string
	}{
		{"GET", "https://api-drive.mypikpak.net/drive/v1/files", "GET:/drive/v1/files"},
		{"POST", "https://user.mypikpak.net/v1/auth/signin", "POST:/v1/auth/signin"},
		{"POST", "https://user.mypikpak.net/v1/shield/captcha/init", "POST:/v1/shield/captcha/init"},
		{"GET", "https://api-drive.mypikpak.net/drive/v1/files?page_token=abc", "GET:/drive/v1/files"},
		{"POST", "https://user.mypikpak.net/v1/auth/token", "POST:/v1/auth/token"},
	}
	for _, tt := range tests {
		t.Run(tt.method+":"+tt.url, func(t *testing.T) {
			got := GetAction(tt.method, tt.url)
			if got != tt.want {
				t.Errorf("GetAction(%q, %q) = %q, want %q", tt.method, tt.url, got, tt.want)
			}
		})
	}
}

func TestGetCaptchaSign(t *testing.T) {
	c := &Common{
		ClientID:      "YNxT9w7GMdWvEOKa",
		ClientVersion: "1.53.2",
		PackageName:   "com.pikcloud.pikpak",
		DeviceID:      "test-device-id",
		Algorithms:    AndroidAlgorithms,
	}

	timestamp, sign := c.GetCaptchaSign()
	if timestamp == "" {
		t.Error("timestamp should not be empty")
	}
	if sign == "" {
		t.Error("sign should not be empty")
	}
	if sign[:2] != "1." {
		t.Errorf("sign should start with '1.', got %q", sign[:2])
	}
	// MD5 hex = 32 chars, plus "1." prefix = 34
	if len(sign) != 34 {
		t.Errorf("sign length should be 34, got %d", len(sign))
	}
}

func TestGenerateDeviceSign(t *testing.T) {
	sign := generateDeviceSign("test-device", "com.pikcloud.pikpak")
	if sign == "" {
		t.Error("device sign should not be empty")
	}
	if len(sign) < len("div101.") {
		t.Error("device sign too short")
	}
	if sign[:7] != "div101." {
		t.Errorf("device sign should start with 'div101.', got %q", sign[:7])
	}

	// Same inputs should produce same output (deterministic)
	sign2 := generateDeviceSign("test-device", "com.pikcloud.pikpak")
	if sign != sign2 {
		t.Error("generateDeviceSign should be deterministic")
	}
}

func TestBuildCustomUserAgent(t *testing.T) {
	ua := BuildCustomUserAgent("dev123", AndroidClientID, AndroidPackageName,
		AndroidSdkVersion, AndroidClientVersion, AndroidPackageName, "user456")
	if ua == "" {
		t.Error("user agent should not be empty")
	}
	// Should contain key fields
	for _, want := range []string{"ANDROID-", "clientid/", "deviceid/dev123", "usrno/user456"} {
		if !contains(ua, want) {
			t.Errorf("user agent should contain %q", want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
