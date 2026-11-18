package _139

import (
	"net/url"
	"testing"
	"time"
)

func presignedURL(signedAt time.Time, expires string) string {
	// signedAt is when the signature was issued (X-Amz-Date); the URL
	// stays valid for the X-Amz-Expires seconds after that moment.
	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Date", signedAt.UTC().Format("20060102T150405Z"))
	q.Set("X-Amz-Expires", expires)
	q.Set("X-Amz-Signature", "deadbeef")
	return "https://obs.example.com/file.mp4?" + q.Encode()
}

func TestLinkExpirationPresignedURL(t *testing.T) {
	// issued now, 15-minute lifetime: expect ~14 minutes (lifetime minus margin)
	u := presignedURL(time.Now(), "900")
	exp := linkExpiration(u)
	if *exp < 13*time.Minute || *exp > 15*time.Minute {
		t.Fatalf("expected ~14min for a 15min presigned URL, got %v", *exp)
	}
}

func TestLinkExpirationPresignedURLCustomLifetime(t *testing.T) {
	// issued now, 1-hour lifetime: expect ~59 minutes, not the conservative default
	u := presignedURL(time.Now(), "3600")
	exp := linkExpiration(u)
	if *exp < 57*time.Minute || *exp > time.Hour {
		t.Fatalf("expected ~59min for a 1h presigned URL, got %v", *exp)
	}
}

func TestLinkExpirationExpiredPresign(t *testing.T) {
	// signature died 1 minute ago: non-positive TTL, the cache entry
	// expires immediately and the next request refetches
	u := presignedURL(time.Now().Add(-16*time.Minute), "900")
	exp := linkExpiration(u)
	if *exp > 0 {
		t.Fatalf("expected non-positive TTL for an expired presigned URL, got %v", *exp)
	}
}

func TestLinkExpirationDyingSignature(t *testing.T) {
	// issued 14.5 min ago with a 15-min lifetime: only 30 seconds of life
	// left, and the 1-minute margin exceeds it, so the TTL must not be
	// positive (caching would serve the dead signature)
	u := presignedURL(time.Now().Add(-(900-30)*time.Second), "900")
	exp := linkExpiration(u)
	if *exp > 0 {
		t.Fatalf("expected non-positive TTL for a 30s-left signature, got %v", *exp)
	}
}

func TestLinkExpirationPlainURL(t *testing.T) {
	// endpoints without presign parameters: conservative default
	exp := linkExpiration("https://download.example.com/file.mp4?t=2&r=1")
	if *exp != defaultLinkExpiration {
		t.Fatalf("expected default %v for a plain URL, got %v", defaultLinkExpiration, *exp)
	}
}
