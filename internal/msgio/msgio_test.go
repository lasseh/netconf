package msgio

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/carlmjohnson/be"
)

var errBroken = errors.New("broken")

// broken fails every call with err.
type broken struct{ err error }

func (b broken) Read([]byte) (int, error)  { return 0, b.err }
func (b broken) Write([]byte) (int, error) { return 0, b.err }
func (b broken) Close() error              { return b.err }

func explained(err error) error { return fmt.Errorf("explained: %w", err) }

func TestErrorsAreExplained(t *testing.T) {
	r := Reader(broken{errBroken}, explained)
	_, err := r.Read(nil)
	be.In(t, "explained", err.Error())
	be.True(t, errors.Is(err, errBroken))
	be.In(t, "explained", r.Close().Error())

	w := Writer(broken{errBroken}, explained)
	_, err = w.Write(nil)
	be.In(t, "explained", err.Error())
	be.In(t, "explained", w.Close().Error())
}

func TestEndOfMessageIsNoFailure(t *testing.T) {
	_, err := Reader(broken{io.EOF}, explained).Read(nil)
	be.True(t, err == io.EOF) //nolint:errorlint // a reader ends with io.EOF itself
}
