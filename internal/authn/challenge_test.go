package authn

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestChallengeRegistrationAuthenticationAndReplay(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := newChallengeStore()
	store.now = func() time.Time { return now }

	for _, ceremony := range []Ceremony{CeremonyRegistration, CeremonyLogin} {
		key, err := store.Put(Challenge{
			Session:  webauthn.SessionData{Challenge: "challenge"},
			Ceremony: ceremony,
			UserID:   42,
		}, string(ceremony))
		if err != nil {
			t.Fatalf("Put(%s) error = %v", ceremony, err)
		}
		if _, err = store.Consume(key, ceremony, 42); err != nil {
			t.Fatalf("Consume(%s) error = %v", ceremony, err)
		}
		if _, err = store.Consume(key, ceremony, 42); !errors.Is(err, ErrChallengeInvalid) {
			t.Fatalf("replayed Consume(%s) error = %v, want ErrChallengeInvalid", ceremony, err)
		}
	}
}

func TestChallengeRejectsExpiredOrWrongBinding(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := newChallengeStore()
	store.now = func() time.Time { return now }

	expired, err := store.Put(Challenge{Ceremony: CeremonyLogin, UserID: 42}, "expired")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(ChallengeTTL)
	if _, err = store.Consume(expired, CeremonyLogin, 42); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("expired challenge error = %v, want ErrChallengeInvalid", err)
	}

	wrongUser, err := store.Put(Challenge{Ceremony: CeremonyRegistration, UserID: 42}, "wrong-user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Consume(wrongUser, CeremonyRegistration, 7); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("wrong-user challenge error = %v, want ErrChallengeInvalid", err)
	}
}

func TestChallengeAdmissionPreservesExistingCeremonyDuringHostileBegins(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := newChallengeStore()
	store.now = func() time.Time { return now }

	legitimate, err := store.Put(Challenge{Ceremony: CeremonyLogin}, "legitimate-client")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxChallengesPerAdmissionKey; i++ {
		if _, err = store.Put(Challenge{Ceremony: CeremonyLogin}, "hostile-client"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 1000; i++ {
		if _, err = store.Put(Challenge{Ceremony: CeremonyLogin}, "hostile-client"); !errors.Is(err, ErrChallengeAdmission) {
			t.Fatalf("hostile Put error = %v, want ErrChallengeAdmission", err)
		}
	}
	if _, err = store.Consume(legitimate, CeremonyLogin, 0); err != nil {
		t.Fatalf("legitimate challenge was not retained: %v", err)
	}
}

func TestChallengeCapacityRejectsNewWithoutEvictingLiveCeremonies(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := newChallengeStore()
	store.now = func() time.Time { return now }

	var oldest string
	for i := 0; i < maxPublicLoginChallenges; i++ {
		key, err := store.Put(Challenge{Ceremony: CeremonyLogin}, fmt.Sprintf("client-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = key
		}
		now = now.Add(time.Nanosecond)
	}

	_, err := store.Put(Challenge{Ceremony: CeremonyLogin}, "new-login-client")
	if !errors.Is(err, ErrChallengeCapacity) {
		t.Fatalf("Put at capacity error = %v, want ErrChallengeCapacity", err)
	}
	registration, err := store.Put(
		Challenge{Ceremony: CeremonyRegistration, UserID: 42},
		"registration:42",
	)
	if err != nil {
		t.Fatalf("authenticated registration was blocked by public login traffic: %v", err)
	}
	if len(store.items) != maxPublicLoginChallenges+1 {
		t.Fatalf("challenge count = %d, want %d", len(store.items), maxPublicLoginChallenges+1)
	}
	if _, err = store.Consume(oldest, CeremonyLogin, 0); err != nil {
		t.Fatalf("oldest live challenge was evicted: %v", err)
	}
	if _, err = store.Consume(registration, CeremonyRegistration, 42); err != nil {
		t.Fatalf("reserved registration challenge failed: %v", err)
	}
}

func TestChallengeTotalCapacityRejectsNewWithoutEviction(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := newChallengeStore()
	store.now = func() time.Time { return now }

	var first string
	for i := 0; i < maxChallengeCount; i++ {
		key, err := store.Put(
			Challenge{Ceremony: CeremonyRegistration, UserID: uint(i + 1)},
			fmt.Sprintf("registration:%d", i+1),
		)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = key
		}
	}
	if _, err := store.Put(
		Challenge{Ceremony: CeremonyRegistration, UserID: maxChallengeCount + 1},
		"registration:overflow",
	); !errors.Is(err, ErrChallengeCapacity) {
		t.Fatalf("Put at total capacity error = %v, want ErrChallengeCapacity", err)
	}
	if _, err := store.Consume(first, CeremonyRegistration, 1); err != nil {
		t.Fatalf("first live challenge was evicted: %v", err)
	}
}

func TestChallengeAdmissionSlotsExpire(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := newChallengeStore()
	store.now = func() time.Time { return now }

	for i := 0; i < maxChallengesPerAdmissionKey; i++ {
		if _, err := store.Put(Challenge{Ceremony: CeremonyLogin}, "client"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(ChallengeTTL)
	if _, err := store.Put(Challenge{Ceremony: CeremonyLogin}, "client"); err != nil {
		t.Fatalf("Put after admission expiry error = %v", err)
	}
	if len(store.items) != 1 {
		t.Fatalf("challenge count after expiry = %d, want 1", len(store.items))
	}
}
