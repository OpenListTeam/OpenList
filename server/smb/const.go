package smb

import "github.com/pkg/errors"

var (
	ErrBadHandle = errors.New("bad handle")
	TestError    = errors.New("test error")
)
