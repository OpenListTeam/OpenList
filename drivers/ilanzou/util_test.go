package ilanzou

import "testing"

func TestAppTokenQueryValue(t *testing.T) {
	got := appTokenQueryValue("alpha:beta+gamma&delta#percent%")
	want := "alpha:beta%2Bgamma%26delta%23percent%25"
	if got != want {
		t.Fatalf("appTokenQueryValue() = %q, want %q", got, want)
	}
}
