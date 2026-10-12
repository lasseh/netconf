package tail

import (
	"strings"
	"testing"

	"github.com/carlmjohnson/be"
)

// The end of what was said is what is kept of it, on one line.
func TestBuffer(t *testing.T) {
	var kept Buffer
	_, _ = kept.Write([]byte(strings.Repeat("x", 2*Kept)))
	_, _ = kept.Write([]byte("\n  the last\n\tline \n"))
	be.Equal(t, Kept, len(kept.buf))
	be.True(t, strings.HasSuffix(kept.String(), "x the last line"))
	be.Equal(t, "", (*Buffer)(nil).String())
}
