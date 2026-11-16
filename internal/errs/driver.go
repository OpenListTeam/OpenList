package errs

import "errors"

var (
	EmptyToken = errors.New("empty token")

	// ErrUnavailableHash indicates the seed does not carry the hash algorithm
	// required by the destination driver, so rapid upload cannot be attempted.
	ErrUnavailableHash = errors.New("required hash is unavailable")
	// ErrEmptyHash indicates a required hash exists but is empty/too short.
	ErrEmptyHash = errors.New("empty hash")
	// ErrHashMismatch indicates the remote side rejected the provided hash, so
	// a full content transfer is required instead of a rapid upload.
	ErrHashMismatch = errors.New("hash mismatch")
	// ErrRapidUploadFailed indicates the driver attempted a rapid upload but
	// could not confirm success.
	ErrRapidUploadFailed = errors.New("rapid upload failed")
)
