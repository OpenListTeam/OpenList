package errs

import "errors"

func UnwrapOrSelf(err error) error {
	// errors.Unwrap has no fallback mechanism
	unwrapped := errors.Unwrap(err)
	if unwrapped == nil {
		return err
	}
	return unwrapped
}
