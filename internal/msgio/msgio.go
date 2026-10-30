// Package msgio is the reader and the writer of a message of a transport
// that knows more about why IO failed than the Framer under it does, such as
// that it was closed from elsewhere.
package msgio

import "io"

// Reader returns r with errors that because has explained.
func Reader(r io.ReadCloser, because func(error) error) io.ReadCloser {
	return reader{r, because}
}

// Writer returns w with errors that because has explained.
func Writer(w io.WriteCloser, because func(error) error) io.WriteCloser {
	return writer{w, because}
}

type (
	reader struct {
		io.ReadCloser
		because func(error) error
	}
	writer struct {
		io.WriteCloser
		because func(error) error
	}
)

func (r reader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	// the end of a message is not a failure
	if err != nil && err != io.EOF { //nolint:errorlint // what a reader ends with, never wrapped
		err = r.because(err)
	}
	return n, err
}

// Close reads the message to its end.  What that fails with is explained
// like the error of a Read: the session closes a message that nobody reads
// without reading it first.
func (r reader) Close() error {
	if err := r.ReadCloser.Close(); err != nil {
		return r.because(err)
	}
	return nil
}

func (w writer) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	if err != nil {
		err = w.because(err)
	}
	return n, err
}

func (w writer) Close() error {
	if err := w.WriteCloser.Close(); err != nil {
		return w.because(err)
	}
	return nil
}
