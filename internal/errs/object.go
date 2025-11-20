package errs

import (
	"errors"

	pkgerr "github.com/pkg/errors"
)

var (
	ObjectNotFound      = errors.New("object not found")
	ObjectAlreadyExists = errors.New("object already exists")
	NotFolder           = errors.New("not a folder")
	NotFile             = errors.New("not a file")
	IgnoredSystemFile   = errors.New("system file upload ignored")
)

func IsObjectNotFound(err error) bool {
	return errors.Is(pkgerr.Cause(err), ObjectNotFound)
}
