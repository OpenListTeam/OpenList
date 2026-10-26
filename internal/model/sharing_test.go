package model

import (
	"testing"
	"time"
)

func TestSharingValid(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name   string
		change func(*Sharing)
		want   bool
	}{
		{name: "below limit", want: true},
		{name: "at limit", change: func(s *Sharing) { s.Accessed = 1 }},
		{name: "over limit", change: func(s *Sharing) { s.Accessed = 2 }},
		{name: "unlimited", change: func(s *Sharing) { s.MaxAccessed = 0; s.Accessed = 10 }, want: true},
		{name: "future expiry", change: func(s *Sharing) { s.Expires = &future }, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Sharing{
				SharingDB: &SharingDB{MaxAccessed: 1},
				Files:     []string{"/file"},
				Creator:   &User{Permission: 1 << 14},
			}
			if tc.change != nil {
				tc.change(s)
			}
			if got := s.Valid(); got != tc.want {
				t.Errorf("Valid() = %v, want %v", got, tc.want)
			}
			if got := s.Valid(false); got != tc.want {
				t.Errorf("Valid(false) = %v, want %v", got, tc.want)
			}
			if !s.Valid(true) {
				t.Error("an already counted visit should remain valid")
			}
		})
	}

	for _, tc := range []struct {
		name   string
		change func(*Sharing)
	}{
		{"disabled", func(s *Sharing) { s.Disabled = true }},
		{"expired", func(s *Sharing) { s.Expires = &past }},
		{"no files", func(s *Sharing) { s.Files = nil }},
		{"no creator", func(s *Sharing) { s.Creator = nil }},
		{"permission revoked", func(s *Sharing) { s.Creator.Permission = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Sharing{
				SharingDB: &SharingDB{MaxAccessed: 1, Accessed: 1},
				Files:     []string{"/file"},
				Creator:   &User{Permission: 1 << 14},
			}
			tc.change(s)
			if s.Valid(true) {
				t.Error("an already counted visit must not bypass other validity checks")
			}
		})
	}
}
