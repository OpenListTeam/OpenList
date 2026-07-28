package authn

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestChallengeAdmissionRateSurvivesBeginFinishChurn(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	limiter := newAdmissionLimiter(maxLoginChallengeAdmissionKeys)
	limiter.now = func() time.Time { return now }
	store := newChallengeStore()
	store.now = func() time.Time { return now }

	const sharedClient = "shared-client"
	if !limiter.Allow(sharedClient) {
		t.Fatal("legitimate begin was not admitted")
	}
	legitimate, err := store.Put(Challenge{Ceremony: CeremonyLogin}, sharedClient)
	if err != nil {
		t.Fatal(err)
	}

	allowed := 0
	for i := 0; i < 1000; i++ {
		if !limiter.Allow(sharedClient) {
			continue
		}
		allowed++
		hostile, putErr := store.Put(Challenge{Ceremony: CeremonyLogin}, sharedClient)
		if putErr != nil {
			t.Fatal(putErr)
		}
		if _, consumeErr := store.Consume(hostile, CeremonyRegistration, 0); !errors.Is(consumeErr, ErrChallengeInvalid) {
			t.Fatalf("hostile invalid finish error = %v, want ErrChallengeInvalid", consumeErr)
		}
	}
	if allowed != challengeAdmissionBurst-1 {
		t.Fatalf("hostile begins admitted = %d, want %d", allowed, challengeAdmissionBurst-1)
	}
	if _, err = store.Consume(legitimate, CeremonyLogin, 0); err != nil {
		t.Fatalf("legitimate challenge was not retained: %v", err)
	}

	now = now.Add(challengeAdmissionInterval)
	if !limiter.Allow(sharedClient) {
		t.Fatal("admission rate did not replenish one request")
	}
}

func TestChallengeAdmissionKeysAreBoundedWithoutEvictingActiveClients(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	limiter := newAdmissionLimiter(maxLoginChallengeAdmissionKeys)
	limiter.now = func() time.Time { return now }

	for i := 0; i < maxLoginChallengeAdmissionKeys; i++ {
		if !limiter.Allow(fmt.Sprintf("client-%d", i)) {
			t.Fatalf("client %d was not admitted", i)
		}
	}
	if limiter.Allow("new-client") {
		t.Fatal("new client was admitted after the identity store reached capacity")
	}
	if !limiter.Allow("client-0") {
		t.Fatal("active client was evicted at identity-store capacity")
	}

	now = now.Add(ChallengeTTL)
	if !limiter.Allow("new-client") {
		t.Fatal("expired admission identities were not released")
	}
}

func TestPublicLoginAdmissionCapacityDoesNotBlockRegistration(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	loginLimiter := newAdmissionLimiter(maxLoginChallengeAdmissionKeys)
	registrationLimiter := newAdmissionLimiter(maxRegisterChallengeAdmissionKeys)
	loginLimiter.now = func() time.Time { return now }
	registrationLimiter.now = func() time.Time { return now }

	for i := 0; i < maxLoginChallengeAdmissionKeys; i++ {
		if !loginLimiter.Allow(fmt.Sprintf("login:%d", i)) {
			t.Fatalf("login client %d was not admitted", i)
		}
	}
	if loginLimiter.Allow("login:overflow") {
		t.Fatal("public login exceeded its admission capacity")
	}
	if !registrationLimiter.Allow("registration:42") {
		t.Fatal("public login admission capacity blocked registration")
	}
}
