// Package socketactivation receives the named listeners used by OpenList.
package socketactivation

import (
	"fmt"
	"net"
	"os"

	"github.com/coreos/go-systemd/v22/activation"
)

type Sockets struct {
	HTTP  net.Listener
	HTTPS net.Listener
	QUIC  *net.UDPConn
}

// Receive consumes the systemd activation environment once. Unrecognized names
// are ignored; each supported name must identify exactly one socket.
func Receive() (*Sockets, error) {
	return fromFiles(activation.Files(true))
}

func fromFiles(files []*os.File) (sockets *Sockets, err error) {
	sockets = &Sockets{}
	defer func() {
		// FileListener and FilePacketConn duplicate the descriptors. Close the
		// original descriptors, including those with unrecognized names.
		for _, file := range files {
			_ = file.Close()
		}
		if err != nil {
			sockets.Close()
		}
	}()
	seen := make(map[string]bool)
	for _, file := range files {
		name := file.Name()
		switch name {
		case "http", "https", "quic":
		default:
			continue
		}
		if seen[name] {
			return sockets, fmt.Errorf("duplicate activation socket %q", name)
		}
		seen[name] = true
		if name == "quic" {
			conn, e := net.FilePacketConn(file)
			if e != nil {
				return sockets, fmt.Errorf("activation socket %q: %w", name, e)
			}
			udp, ok := conn.(*net.UDPConn)
			if !ok {
				_ = conn.Close()
				return sockets, fmt.Errorf("activation socket %q must be UDP", name)
			}
			sockets.QUIC = udp
			continue
		}
		listener, e := net.FileListener(file)
		if e != nil {
			return sockets, fmt.Errorf("activation socket %q: %w", name, e)
		}
		if _, ok := listener.(*net.TCPListener); !ok {
			_ = listener.Close()
			return sockets, fmt.Errorf("activation socket %q must be a TCP listener", name)
		}
		if name == "http" {
			sockets.HTTP = listener
		} else {
			sockets.HTTPS = listener
		}
	}
	return sockets, nil
}

func (s *Sockets) Close() {
	if s.HTTP != nil {
		_ = s.HTTP.Close()
	}
	if s.HTTPS != nil {
		_ = s.HTTPS.Close()
	}
	if s.QUIC != nil {
		_ = s.QUIC.Close()
	}
}
