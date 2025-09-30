package errs

import "errors"

func UnwrapOrSelf(err error) error {
	// errors.Unwrap没有fallback机制
	unwrapped := errors.Unwrap(err)
	if unwrapped == nil {
		return err
	}
	return unwrapped
}
