package directupload

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegisterAndGet(t *testing.T) {
	token, err := Register(&Session{
		UserID:  1,
		Path:    "/storage/a.bin",
		Expires: time.Now().Add(time.Hour),
		Finish:  func(context.Context, []string) error { return nil },
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if token == "" {
		t.Fatal("Register returned an empty token")
	}

	session, err := Get(token, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if session.Path != "/storage/a.bin" {
		t.Fatalf("Get path = %q, want %q", session.Path, "/storage/a.bin")
	}

	if _, err = Get(token, 2); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("Get with another user = %v, want ErrNotOwner", err)
	}

	Delete(token)
	if _, err = Get(token, 1); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrSessionNotFound", err)
	}
}

func TestRegisterRequiresFinish(t *testing.T) {
	if _, err := Register(&Session{UserID: 1}); err == nil {
		t.Fatal("Register without a finish handler should fail")
	}
}

func TestExpireDropsSessionAndAborts(t *testing.T) {
	var aborted atomic.Int32
	token, err := Register(&Session{
		UserID:  1,
		Expires: time.Now().Add(-time.Second),
		Finish:  func(context.Context, []string) error { return nil },
		Abort:   func(context.Context) { aborted.Add(1) },
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	manager.expire(time.Now())

	if aborted.Load() != 1 {
		t.Fatalf("Abort calls = %d, want 1", aborted.Load())
	}
	if _, err = Get(token, 1); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get after expiry = %v, want ErrSessionNotFound", err)
	}
}
