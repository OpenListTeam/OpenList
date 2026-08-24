package authn

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func AdmissionClientKey(r *http.Request, trustedProxies []string) (string, error) {
	if r == nil {
		return "", errors.New("passkey admission request is required")
	}
	prefixes, err := parseTrustedProxies(trustedProxies)
	if err != nil {
		return "", err
	}
	if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && local.Network() == "unix" {
		client, err := unixForwardedClientAddress(r, prefixes)
		if err != nil {
			return "", err
		}
		return admissionAddressKey(client), nil
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "", errors.New("passkey admission client address is invalid")
	}
	peer = peer.Unmap()

	client := peer
	if addressInPrefixes(peer, prefixes) {
		client = forwardedClientAddress(r, peer, prefixes)
	}
	return admissionAddressKey(client), nil
}

func parseTrustedProxies(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, addressErr := netip.ParseAddr(value)
			if addressErr != nil {
				return nil, errors.New("passkey trusted proxy address is invalid")
			}
			address = address.Unmap()
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		if prefix.Bits() == 0 {
			return nil, errors.New("passkey trusted proxy cannot include every address")
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func forwardedClientAddress(r *http.Request, peer netip.Addr, trustedProxies []netip.Prefix) netip.Addr {
	if forwarded := r.Header.Values("X-Forwarded-For"); len(forwarded) > 0 {
		addresses := strings.Split(strings.Join(forwarded, ","), ",")
		leftmost := peer
		for i := len(addresses) - 1; i >= 0; i-- {
			address, err := netip.ParseAddr(strings.TrimSpace(addresses[i]))
			if err != nil {
				return peer
			}
			address = address.Unmap()
			leftmost = address
			if !addressInPrefixes(address, trustedProxies) {
				return address
			}
		}
		return leftmost
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		if address, err := netip.ParseAddr(realIP); err == nil {
			return address.Unmap()
		}
	}
	return peer
}

func unixForwardedClientAddress(r *http.Request, trustedProxies []netip.Prefix) (netip.Addr, error) {
	if forwarded := r.Header.Values("X-Forwarded-For"); len(forwarded) > 0 {
		addresses := strings.Split(strings.Join(forwarded, ","), ",")
		parsed := make([]netip.Addr, len(addresses))
		for i, value := range addresses {
			address, err := netip.ParseAddr(strings.TrimSpace(value))
			if err != nil {
				return netip.Addr{}, errors.New("passkey forwarded client address is invalid")
			}
			parsed[i] = address.Unmap()
		}
		var client netip.Addr
		for i := len(parsed) - 1; i >= 0; i-- {
			client = parsed[i]
			if !addressInPrefixes(client, trustedProxies) {
				return client, nil
			}
		}
		return client, nil
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		address, err := netip.ParseAddr(realIP)
		if err != nil {
			return netip.Addr{}, errors.New("passkey forwarded client address is invalid")
		}
		return address.Unmap(), nil
	}
	return netip.Addr{}, errors.New("passkey Unix socket requests require a forwarded client address")
}

func admissionAddressKey(address netip.Addr) string {
	if address.Is6() {
		return netip.PrefixFrom(address, 64).Masked().String()
	}
	return address.String()
}

func addressInPrefixes(address netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
