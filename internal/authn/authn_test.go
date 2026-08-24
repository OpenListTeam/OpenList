package authn

import (
	"crypto/sha256"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
)

func TestAuthnConfigRequiresUserVerificationAndPinsOrigin(t *testing.T) {
	instance, err := NewAuthnInstanceForURL("https://files.example.com/openlist", "OpenList")
	if err != nil {
		t.Fatal(err)
	}
	if got := instance.Config.RPID; got != "files.example.com" {
		t.Fatalf("RPID = %q, want files.example.com", got)
	}
	if got := instance.Config.RPOrigins; len(got) != 1 || got[0] != "https://files.example.com" {
		t.Fatalf("RPOrigins = %#v, want exact deployment origin", got)
	}
	if got := instance.Config.AuthenticatorSelection.UserVerification; got != protocol.VerificationRequired {
		t.Fatalf("UserVerification = %q, want required", got)
	}
	if !instance.Config.Timeouts.Login.Enforce || !instance.Config.Timeouts.Registration.Enforce {
		t.Fatal("server-side WebAuthn timeouts are not enforced")
	}
}

func TestAuthnConfigRequiresHTTPSOutsideLocalhost(t *testing.T) {
	if _, err := NewAuthnInstanceForURL("http://files.example.com", "OpenList"); err == nil {
		t.Fatal("non-local HTTP origin was accepted")
	}
	if _, err := NewAuthnInstanceForURL("http://localhost:5244", "OpenList"); err != nil {
		t.Fatalf("localhost development origin rejected: %v", err)
	}
}

func TestAuthnRejectsWrongOriginAndRPID(t *testing.T) {
	instance, err := NewAuthnInstanceForURL("https://files.example.com", "OpenList")
	if err != nil {
		t.Fatal(err)
	}
	clientData := protocol.CollectedClientData{
		Type:      protocol.AssertCeremony,
		Challenge: "challenge",
		Origin:    "https://attacker.example",
	}
	if err = clientData.Verify(
		"challenge",
		protocol.AssertCeremony,
		instance.Config.RPOrigins,
		nil,
		instance.Config.RPTopOriginVerificationMode,
	); err == nil {
		t.Fatal("wrong origin was accepted")
	}

	wrongRP := sha256.Sum256([]byte("attacker.example"))
	expectedRP := sha256.Sum256([]byte(instance.Config.RPID))
	authenticatorData := protocol.AuthenticatorData{
		RPIDHash: wrongRP[:],
		Flags:    protocol.FlagUserPresent | protocol.FlagUserVerified,
	}
	if err = authenticatorData.Verify(expectedRP[:], nil, true, true); err == nil {
		t.Fatal("wrong RP ID hash was accepted")
	}
}
