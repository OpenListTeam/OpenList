package wopi

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrBadUTF7 is returned to indicate invalid modified UTF-7 encoding.
var ErrBadUTF7 = errors.New("utf7: bad utf-7 encoding")

const modifiedbase64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var u7enc = base64.NewEncoding(modifiedbase64)

// UTF7Decode decodes a modified UTF-7 string (used by X-WOPI-SuggestedTarget)
func UTF7Decode(s string) (string, error) {
	var buf bytes.Buffer
	buf.Grow(len(s))

	for len(s) > 0 {
		// Find the next '&'
		i := strings.IndexByte(s, '&')
		if i < 0 {
			// No more encoded sequences, output the rest directly
			buf.WriteString(s)
			break
		}

		// Output the part before '&'
		buf.WriteString(s[:i])
		s = s[i+1:] // skip '&'

		// Find the end of the modified Base64 sequence (marked by '-')
		j := strings.IndexByte(s, '-')
		if j < 0 {
			return "", ErrBadUTF7
		}

		if j == 0 {
			// '&-' encodes '&'
			buf.WriteByte('&')
			s = s[1:]
			continue
		}

		// Decode the modified Base64 sequence
		b64 := s[:j]
		s = s[j+1:] // skip '-'

		// The base64 string may need padding
		switch len(b64) % 4 {
		case 2:
			b64 += "=="
		case 3:
			b64 += "="
		}

		decoded, err := u7enc.DecodeString(b64)
		if err != nil {
			return "", ErrBadUTF7
		}

		// Convert UTF-16BE bytes to runes
		runes, err := utf16beToRunes(decoded)
		if err != nil {
			return "", err
		}

		for _, r := range runes {
			buf.WriteRune(r)
		}
	}

	return buf.String(), nil
}

func utf16beToRunes(b []byte) ([]rune, error) {
	if len(b)%2 != 0 {
		return nil, ErrBadUTF7
	}

	u16s := make([]uint16, len(b)/2)
	for i := range u16s {
		u16s[i] = uint16(b[i*2])<<8 | uint16(b[i*2+1])
	}

	runes := utf16.Decode(u16s)
	return runes, nil
}

// IsValidWopiSuggestedTarget checks if the X-WOPI-SuggestedTarget is a valid filename
func IsValidWopiSuggestedTarget(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	// Check for invalid characters
	invalidChars := []byte{'<', '>', ':', '"', '|', '?', '*'}
	for _, c := range invalidChars {
		if strings.ContainsRune(name, rune(c)) {
			return false
		}
	}
	return true
}

// Ensure UTF-8 encoding is used
var _ = utf8.RuneError
