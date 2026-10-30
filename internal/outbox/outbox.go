// Package outbox is the queue of a scripted device: the messages it has to
// send, written one at a time, so that a test never waits for the client to
// read.
package outbox

import (
	"sync"
	"time"
)

// Queue holds the messages still to be sent.  Make one with New.
type Queue struct {
	mu sync.Mutex
	// wake signals a change to anything below
	wake *sync.Cond
	msgs []string
	// writing says that a message left the queue and is not written yet
	writing bool
	stopped bool
}

// New returns an empty queue.
func New() *Queue {
	q := &Queue{}
	q.wake = sync.NewCond(&q.mu)
	return q
}

// Run writes the queued messages with send, one at a time, until the queue
// is stopped or send fails.  The queue is stopped when it returns.
func (q *Queue) Run(send func(msg string) error) {
	defer q.Stop()
	for {
		q.mu.Lock()
		q.writing = false
		q.wake.Broadcast()
		for len(q.msgs) == 0 && !q.stopped {
			q.wake.Wait()
		}
		if q.stopped {
			q.mu.Unlock()
			return
		}
		msg := q.msgs[0]
		q.msgs = q.msgs[1:]
		q.writing = true
		q.mu.Unlock()

		if send(msg) != nil {
			return
		}
	}
}

// Push queues msgs behind what is queued.
func (q *Queue) Push(msgs ...string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.msgs = append(q.msgs, msgs...)
	q.wake.Broadcast()
}

// Stop ends Run, behind the message being written.
func (q *Queue) Stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped = true
	q.wake.Broadcast()
}

// Wait waits until every message queued is written or the queue is stopped.
// It reports false when that took longer than timeout.
func (q *Queue) Wait(timeout time.Duration) bool {
	expired := false
	timer := time.AfterFunc(timeout, func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		expired = true
		q.wake.Broadcast()
	})
	defer timer.Stop()

	q.mu.Lock()
	defer q.mu.Unlock()
	for (len(q.msgs) > 0 || q.writing) && !q.stopped && !expired {
		q.wake.Wait()
	}
	return !expired
}
