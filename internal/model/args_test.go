package model

import (
	"testing"
	"time"
)

func TestLinkCloneTransfersOwnershipWithoutCachePolicy(t *testing.T) {
	ttl := time.Minute
	source := &Link{URL: "https://example.test/file", Expiration: &ttl}

	clone := source.Clone()
	if clone.URL != source.URL {
		t.Fatal("clone did not preserve transport data")
	}
	if clone.Expiration != nil {
		t.Fatal("clone inherited source cache policy")
	}
	if err := clone.Close(); err != nil {
		t.Fatal(err)
	}
	if !source.Expired() {
		t.Fatal("closing clone did not release its source")
	}
}
