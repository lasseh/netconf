package netconf

import (
	"context"
	"fmt"
	"sync"
)

// pendingReply is an operation waiting for its reply.
type pendingReply struct {
	// msg has room for the one reply so the receive loop never blocks on it
	msg chan *Message
	ctx context.Context

	// stream hands the live transport reader to the caller instead of
	// buffering the reply (see Session.Stream)
	stream bool

	// sent is set once the rpc was handed to the transport, after which the
	// server may answer it even if we stop waiting
	sent bool

	// forgotten is set once nobody waits for the reply any more
	forgotten bool
}

// replyTable correlates replies to the operations waiting for them.
type replyTable struct {
	mu      sync.Mutex
	pending map[string]*pendingReply

	// closing refuses new operations, see Session.Close
	closing bool

	// orphans counts operations that were sent and then given up on before
	// their reply arrived.  Their replies may still come in.
	orphans int
}

func newReplyTable() *replyTable {
	return &replyTable{pending: make(map[string]*pendingReply)}
}

// register adds req to the operations awaiting a reply.  internal marks the
// session's own close-session, the one operation allowed while closing.
func (t *replyTable) register(msgID string, req *pendingReply, internal bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closing && !internal {
		return ErrClosed
	}
	if _, ok := t.pending[msgID]; ok {
		return fmt.Errorf("rpc with message-id %q is already pending", msgID)
	}
	t.pending[msgID] = req
	return nil
}

// markSent records that req is going out on the wire.
func (t *replyTable) markSent(req *pendingReply) {
	t.mu.Lock()
	defer t.mu.Unlock()
	req.sent = true
}

// notSent records that req did not go out after all.  Its caller may have
// given up on it in the meantime, which counted it as an orphan; it is not
// one, no reply will come for it.
func (t *replyTable) notSent(req *pendingReply) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !req.sent {
		return
	}
	req.sent = false
	if req.forgotten && t.orphans > 0 {
		t.orphans--
	}
}

// forget gives up on req.  If its reply was already resolved this does
// nothing, as msgID may by now belong to another operation.
func (t *replyTable) forget(msgID string, req *pendingReply) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pending[msgID] != req {
		return
	}
	delete(t.pending, msgID)
	req.forgotten = true
	if req.sent {
		t.orphans++
	}
}

// resolve removes and returns the operation a reply with msgID answers, or
// nil when nobody waits for it.  late then says whether that was to be
// expected: an operation was given up on and its answer could still come.
//
// A server that could not parse an rpc replies without a message-id
// (RFC6241 sec 4.2).  Such a reply is taken to answer the only outstanding
// operation, unless a reply to an abandoned one may still be on its way.
func (t *replyTable) resolve(msgID string) (req *pendingReply, late bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if msgID == "" {
		if t.orphans > 0 || len(t.pending) != 1 {
			return nil, t.settleOrphan()
		}
		for id := range t.pending {
			msgID = id
		}
	}

	req, ok := t.pending[msgID]
	if !ok {
		return nil, t.settleOrphan()
	}
	delete(t.pending, msgID)
	return req, false
}

// settleOrphan accounts for a reply nobody waits for, and reports whether it
// can be the late answer to an abandoned operation.
func (t *replyTable) settleOrphan() bool {
	if t.orphans > 0 {
		t.orphans--
		return true
	}
	return false
}

// close refuses operations from now on.
func (t *replyTable) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closing = true
}

func (t *replyTable) isClosing() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closing
}
