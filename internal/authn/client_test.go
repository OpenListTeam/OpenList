package authn

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdmissionClientKeyRejectsUntrustedForwardingHeaders(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.10:12345"
	request.Header.Set("X-Forwarded-For", "198.51.100.20")

	key, err := AdmissionClientKey(request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "192.0.2.10" {
		t.Fatalf("admission key = %q, want direct peer", key)
	}
}

func TestAdmissionClientKeyRejectsTrustAllProxyRanges(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.10:12345"

	for _, trusted := range []string{"0.0.0.0/0", "::/0"} {
		if _, err := AdmissionClientKey(request, []string{trusted}); err == nil {
			t.Fatalf("trusted proxy %q error = %v, want rejection", trusted, err)
		}
	}
}

func TestAdmissionClientKeyUsesFirstUntrustedAddressFromTrustedProxy(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.10:12345"
	request.Header.Set("X-Forwarded-For", "203.0.113.5, 198.51.100.20")

	key, err := AdmissionClientKey(request, []string{"192.0.2.10/32", "198.51.100.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if key != "203.0.113.5" {
		t.Fatalf("admission key = %q, want first untrusted client", key)
	}
}

func TestAdmissionClientKeyDoesNotMixForwardedHeaderSources(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.10:12345"
	request.Header.Set("X-Forwarded-For", "198.51.100.20")
	request.Header.Set("X-Real-IP", "203.0.113.5")

	key, err := AdmissionClientKey(request, []string{"192.0.2.10/32", "198.51.100.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if key != "198.51.100.20" {
		t.Fatalf("admission key = %q, want X-Forwarded-For address", key)
	}
}

func TestAdmissionClientKeyGroupsIPv6Prefix(t *testing.T) {
	first := httptest.NewRequest("GET", "/", nil)
	first.RemoteAddr = "[2001:db8:1234:5678::1]:12345"
	second := httptest.NewRequest("GET", "/", nil)
	second.RemoteAddr = "[2001:db8:1234:5678::ffff]:54321"
	other := httptest.NewRequest("GET", "/", nil)
	other.RemoteAddr = "[2001:db8:1234:5679::1]:12345"

	firstKey, err := AdmissionClientKey(first, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := AdmissionClientKey(second, nil)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := AdmissionClientKey(other, nil)
	if err != nil {
		t.Fatal(err)
	}
	if firstKey != secondKey {
		t.Fatalf("same IPv6 /64 keys differ: %q != %q", firstKey, secondKey)
	}
	if firstKey == otherKey {
		t.Fatalf("different IPv6 /64 keys match: %q", firstKey)
	}
}

func TestAdmissionClientKeySupportsUnixSocketProxyChain(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "@"
	request.Header.Set("X-Forwarded-For", "198.51.100.20, 203.0.113.5")
	request = request.WithContext(context.WithValue(
		request.Context(),
		http.LocalAddrContextKey,
		&net.UnixAddr{Name: "/run/openlist.sock", Net: "unix"},
	))

	key, err := AdmissionClientKey(request, []string{"203.0.113.5/32"})
	if err != nil {
		t.Fatal(err)
	}
	if key != "198.51.100.20" {
		t.Fatalf("admission key = %q, want first untrusted client address", key)
	}
}

func TestAdmissionClientKeyRejectsMalformedUnixSocketForwarding(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "@"
	request.Header.Set("X-Forwarded-For", "invalid, 203.0.113.5")
	request = request.WithContext(context.WithValue(
		request.Context(),
		http.LocalAddrContextKey,
		&net.UnixAddr{Name: "/run/openlist.sock", Net: "unix"},
	))

	if _, err := AdmissionClientKey(request, nil); err == nil {
		t.Fatal("malformed Unix-socket forwarding was accepted")
	}
}

func TestAdmissionClientKeyRejectsUnixSocketTrafficWithoutForwarding(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "@"
	request = request.WithContext(context.WithValue(
		request.Context(),
		http.LocalAddrContextKey,
		&net.UnixAddr{Name: "/run/openlist.sock", Net: "unix"},
	))

	if _, err := AdmissionClientKey(request, nil); err == nil {
		t.Fatal("Unix-socket traffic without a forwarded client address was accepted")
	}
}
