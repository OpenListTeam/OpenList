package db

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"
)

func TestPasskeyLifecyclePreservesPasswordLogin(t *testing.T) {
	previousDB := db
	testDB, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db = testDB
	t.Cleanup(func() { db = previousDB })
	if err = testDB.AutoMigrate(new(model.User)); err != nil {
		t.Fatal(err)
	}

	user := &model.User{Username: "passkey-user", Authn: "[]"}
	user.SetPassword("still-works")
	if err = testDB.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	registeredAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	credential := &webauthn.Credential{
		ID:        []byte("credential-id"),
		PublicKey: []byte("public-key"),
		Authenticator: webauthn.Authenticator{
			SignCount: 1,
		},
	}
	if err = RegisterAuthn(user, credential, "MacBook Touch ID", registeredAt); err != nil {
		t.Fatalf("RegisterAuthn() error = %v", err)
	}
	secondCredential := &webauthn.Credential{
		ID:        []byte("credential-id-2"),
		PublicKey: []byte("public-key-2"),
	}
	if err = RegisterAuthn(user, secondCredential, "", registeredAt); err != nil {
		t.Fatalf("second RegisterAuthn() error = %v", err)
	}
	if err = RenameAuthn(user, "Y3JlZGVudGlhbC1pZC0y", "YubiKey"); err != nil {
		t.Fatalf("RenameAuthn() error = %v", err)
	}

	credential.Authenticator.SignCount = 2
	usedAt := registeredAt.Add(time.Hour)
	if err = UpdateAuthnUsage(user.ID, credential, usedAt); err != nil {
		t.Fatalf("UpdateAuthnUsage() error = %v", err)
	}

	current, err := GetUserById(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	passkeys, err := current.PasskeyCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if len(passkeys) != 2 || passkeys[0].Name != "MacBook Touch ID" || passkeys[1].Name != "YubiKey" {
		t.Fatalf("stored passkeys = %#v", passkeys)
	}
	if passkeys[0].Authenticator.SignCount != 2 || passkeys[0].LastUsedAt == nil || !passkeys[0].LastUsedAt.Equal(usedAt) {
		t.Fatalf("authentication metadata not persisted: %#v", passkeys[0])
	}

	staleCredential := *credential
	staleCredential.Authenticator.SignCount = 1
	if err = UpdateAuthnUsage(user.ID, &staleCredential, usedAt.Add(time.Hour)); !errors.Is(err, ErrPasskeyCounterDidNotAdvance) {
		t.Fatalf("stale counter update error = %v, want ErrPasskeyCounterDidNotAdvance", err)
	}
	current, err = GetUserById(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	passkeys, err = current.PasskeyCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if passkeys[0].Authenticator.SignCount != 2 || !passkeys[0].LastUsedAt.Equal(usedAt) {
		t.Fatalf("stale counter overwrote newer authentication state: %#v", passkeys[0])
	}

	for expected := uint32(4); expected <= 100; expected += 2 {
		higher := *credential
		higher.Authenticator.SignCount = expected
		lower := *credential
		lower.Authenticator.SignCount = expected - 1

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = UpdateAuthnUsage(user.ID, &higher, usedAt)
		}()
		go func() {
			defer wg.Done()
			_ = UpdateAuthnUsage(user.ID, &lower, usedAt)
		}()
		wg.Wait()

		current, err = GetUserById(user.ID)
		if err != nil {
			t.Fatal(err)
		}
		passkeys, err = current.PasskeyCredentials()
		if err != nil {
			t.Fatal(err)
		}
		if passkeys[0].Authenticator.SignCount != expected {
			t.Fatalf("concurrent counter update = %d, want %d", passkeys[0].Authenticator.SignCount, expected)
		}
	}
	if err = current.ValidateRawPassword("still-works"); err != nil {
		t.Fatalf("existing password login regressed: %v", err)
	}

	userUpdate := *current
	userUpdate.Authn = "[]"
	userUpdate.BasePath = "/updated"
	if err = UpdateUser(&userUpdate); err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}
	current, err = GetUserById(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := current.WebAuthnCredentials(); len(got) != 2 {
		t.Fatalf("unrelated user update erased passkeys: %#v", got)
	}

	if err = RemoveAuthn(current, "Y3JlZGVudGlhbC1pZA=="); err != nil {
		t.Fatalf("RemoveAuthn() error = %v", err)
	}
	current, err = GetUserById(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := current.WebAuthnCredentials(); len(got) != 1 || string(got[0].ID) != "credential-id-2" {
		t.Fatalf("revoked credential remains usable or sibling was removed: %#v", got)
	}
	if err = RemoveAuthn(current, "Y3JlZGVudGlhbC1pZA=="); err == nil {
		t.Fatal("revoking an already-revoked credential succeeded")
	}
}
