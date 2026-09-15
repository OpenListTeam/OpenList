package bootstrap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func testCertificate(t *testing.T) (string, string, *tls.Config) {
	t.Helper()
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	cert := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return certFile, keyFile, &tls.Config{RootCAs: roots}
}

func TestServeActivatedEndpoints(t *testing.T) {
	certFile, keyFile, clientTLS := testCertificate(t)
	for _, protocol := range []string{"http", "https", "quic"} {
		t.Run(protocol, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, r.Proto)
			})
			result := make(chan error, 1)
			client := &http.Client{Timeout: 5 * time.Second}
			var address string
			var shutdown func(context.Context) error
			want := "HTTP/1.1"
			if protocol == "quic" {
				conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				address = "https://" + conn.LocalAddr().String()
				// An invalid configured address proves Serve uses the supplied socket.
				srv := &http3.Server{Addr: "invalid", Handler: handler}
				defer srv.Close()
				shutdown = srv.Shutdown
				transport := &http3.Transport{TLSClientConfig: clientTLS}
				defer transport.Close()
				client.Transport = transport
				want = "HTTP/3.0"
				go func() { result <- serveQUIC(srv, conn, certFile, keyFile) }()
			} else {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				address = protocol + "://" + listener.Addr().String()
				srv := &http.Server{Addr: "invalid", Handler: handler}
				defer srv.Close()
				shutdown = srv.Shutdown
				transport := &http.Transport{TLSClientConfig: clientTLS}
				defer transport.CloseIdleConnections()
				client.Transport = transport
				go func() {
					if protocol == "https" {
						result <- serveHTTPS(srv, listener, certFile, keyFile)
					} else {
						result <- serveHTTP(srv, listener)
					}
				}()
			}
			response, err := client.Get(address)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || string(body) != want {
				t.Fatalf("response %q, %v; want %q", body, err, want)
			}
			client.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, http.ErrServerClosed) {
					t.Fatalf("unexpected Serve result: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("Serve did not stop")
			}
		})
	}
}

func TestTLSFailureClosesActivatedSockets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := serveHTTPS(&http.Server{}, listener, "missing-cert", "missing-key"); err == nil {
		t.Fatal("expected certificate error")
	}
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("HTTPS listener not closed: %v", err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := serveQUIC(&http3.Server{}, conn, "missing-cert", "missing-key"); err == nil {
		t.Fatal("expected certificate error")
	}
	if _, _, err := conn.ReadFrom(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("QUIC socket not closed: %v", err)
	}
}

func TestServeWithoutActivationUsesConfiguredAddress(t *testing.T) {
	if err := serveHTTP(&http.Server{Addr: "invalid"}, nil); err == nil {
		t.Fatal("expected configured address to be used")
	}
	certFile, keyFile, _ := testCertificate(t)
	if err := serveHTTPS(&http.Server{Addr: "invalid"}, nil, certFile, keyFile); err == nil {
		t.Fatal("expected configured HTTPS address to be used")
	}
	if err := serveQUIC(&http3.Server{Addr: "invalid"}, nil, certFile, keyFile); err == nil {
		t.Fatal("expected configured QUIC address to be used")
	}
}
