package bootstrap

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"

	"github.com/quic-go/quic-go/http3"
)

func serveHTTP(srv *http.Server, listener net.Listener) error {
	if listener == nil {
		return srv.ListenAndServe()
	}
	return srv.Serve(listener)
}

func serveHTTPS(srv *http.Server, listener net.Listener, certFile, keyFile string) error {
	if listener == nil {
		return srv.ListenAndServeTLS(certFile, keyFile)
	}
	// ServeTLS can fail while loading certificates, before Serve owns the listener.
	defer listener.Close()
	return srv.ServeTLS(listener, certFile, keyFile)
}

// The caller closes conn after Shutdown has finished draining connections.
func serveQUIC(srv *http3.Server, conn *net.UDPConn, certFile, keyFile string) error {
	if conn == nil {
		return srv.ListenAndServeTLS(certFile, keyFile)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err == nil {
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
		err = srv.Serve(conn)
	}
	// http3.Server does not close supplied packet connections. Close immediately
	// on startup/serving failure, but keep UDP open during graceful shutdown.
	if !errors.Is(err, http.ErrServerClosed) {
		_ = conn.Close()
	}
	return err
}
