// Package dlnaflags provides utility functionality to DLNA.
package dlnaflags

import (
	"time"
)

// Options is the type for DLNA serving options.
type Options struct {
	ListenAddr       string
	FriendlyName     string
	LogTrace         bool
	InterfaceNames   []string
	AnnounceInterval time.Duration
	RootDir          string
}
