package middlewares

import "net"

// stripHostPort removes the port from a host string, correctly handling
// both IPv4 (e.g. "example.com:8080") and bracketed IPv6 addresses
// (e.g. "[::1]:5244") by delegating to net.SplitHostPort.
// If no port is present, or the input is malformed, the host is returned
// unchanged; callers should treat a no-match result from the database as
// the safe fallback in both cases.
func stripHostPort(host string) string {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		// No port present, or the host is already a bare address.
		return host
	}
	return h
}
