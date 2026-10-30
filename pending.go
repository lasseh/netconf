package netconf

import (
	"context"
	"fmt"
	"sync"
)

// pendingReply is an operation waiting for its reply.
type pendingReply struct {
	// id is the message-id of the operation
	id string

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

	// holder is the message-id of the request that has the turn and is on
	// its way, on a session that has turns, see Session.send
	holder string
	// early holds the requests that lost the turn to another message.  Their
	// replies are still to come and end no turn.
	early map[string]struct{}
	// replied is the message-id of the reply being read, empty for any other
	// message
	replied string
}

func newReplyTable() *replyTable {
	return &replyTable{
		pending: make(map[string]*pendingReply),
		early:   make(map[string]struct{}),
	}
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
	req.id = msgID
	t.pending[msgID] = req
	return nil
}

// markSent records that req is going out on the wire.
func (t *replyTable) markSent(req *pendingReply) {
	t.mu.Lock()
	defer t.mu.Unlock()
	req.sent = true
}

// holdTurn records that req has the turn and is going out on the wire.
func (t *replyTable) holdTurn(req *pendingReply) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.holder = req.id
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
	if t.holder == req.id {
		t.holder = ""
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
// (RFC6241 sec 4.2).  Such a reply is taken to answer the only operation
// that was sent, unless a reply to an abandoned one may still be on its way.
// An operation that waits to be sent is not in question: the server has seen
// nothing of it.
func (t *replyTable) resolve(msgID string) (req *pendingReply, late bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if msgID == "" {
		if t.orphans > 0 {
			return nil, t.settleOrphan()
		}
		for id, req := range t.pending {
			if !req.sent {
				continue
			}
			if msgID != "" {
				return nil, false
			}
			msgID = id
		}
		if msgID == "" {
			return nil, false
		}
	}

	t.replied = msgID
	req, ok := t.pending[msgID]
	if !ok {
		return nil, t.settleOrphan()
	}
	delete(t.pending, msgID)
	return req, false
}

// turnOver reports whether the message that has just arrived whole, which is
// no notification, ends the turn of the request that has it.
//
// Any such message does, one that nobody can read included: a session that
// waited for a better one would send nothing more.  The request that lost
// its turn that way is still to be answered, and that reply ends nothing, or
// each reply would end the turn of the request behind its own for as long as
// requests keep waiting.  With no request on its way there is no turn to end.
func (t *replyTable) turnOver() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	replied := t.replied
	t.replied = ""
	if _, ok := t.early[replied]; ok {
		delete(t.early, replied)
		return false
	}
	if t.holder == "" {
		return false
	}
	if t.holder != replied {
		t.early[t.holder] = struct{}{}
	}
	t.holder = ""
	return true
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
