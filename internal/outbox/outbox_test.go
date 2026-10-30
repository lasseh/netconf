package outbox

import (
	"errors"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
)

func TestWait(t *testing.T) {
	q := New()
	be.True(t, q.Wait(time.Hour)) // nothing is queued

	release := make(chan struct{})
	var sent []string
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		q.Run(func(msg string) error {
			<-release
			sent = append(sent, msg)
			return nil
		})
	}()

	q.Push("one", "two")
	be.False(t, q.Wait(10*time.Millisecond))

	close(release)
	be.True(t, q.Wait(5*time.Second))

	q.Stop()
	<-ran
	be.AllEqual(t, []string{"one", "two"}, sent)
}

// A device that cannot write leaves nothing to wait for.
func TestWaitForAQueueThatFailed(t *testing.T) {
	q := New()
	go q.Run(func(string) error { return errors.New("the client is gone") })

	q.Push("one", "two")
	be.True(t, q.Wait(5*time.Second))
}
