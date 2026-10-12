// Package tail keeps the end of what a process or a device wrote to its
// standard error, for the error of one that gave up: the reason is what it
// said last.
package tail

import (
	"bytes"
	"strings"
	"sync"
)

// Kept is how much is kept, in bytes.
const Kept = 4 << 10

// Buffer is a writer that keeps the end of what is written to it.  It is
// safe for a writer and a reader at the same time.
type Buffer struct {
	mu  sync.Mutex
	buf []byte
}

// Write implements io.Writer.  It never fails.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf = append(b.buf, p...)
	if len(b.buf) > Kept {
		b.buf = append(b.buf[:0], b.buf[len(b.buf)-Kept:]...)
	}
	return len(p), nil
}

// String returns what was kept, on one line.  A nil buffer kept nothing.
func (b *Buffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(strings.Fields(string(bytes.TrimSpace(b.buf))), " ")
}
