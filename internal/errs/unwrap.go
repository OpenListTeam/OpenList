package errs

func UnwrapOrSelf(err error) error {
	u, ok := err.(interface {
		Unwrap() error
	})
	if !ok {
		return err
	}
	return u.Unwrap()
}
