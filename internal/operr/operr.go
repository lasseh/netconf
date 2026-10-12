// Package operr names the operation in the errors of the operations.
package operr

// Error is the error of an operation, with the name of the operation in
// front of it.
type Error struct {
	Op  string
	Err error
}

func (e *Error) Error() string { return e.Op + ": " + e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

// Named returns err with the name of the operation in front of it, and nil
// for no error.
func Named(op string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Op: op, Err: err}
}

// Bare returns the error of an operation without the name of the operation,
// for a caller that names what it was doing itself.  Any other error is
// returned as it is.
func Bare(err error) error {
	if named, ok := err.(*Error); ok { //nolint:errorlint // the name is the outermost thing of an operation's error
		return named.Err
	}
	return err
}
