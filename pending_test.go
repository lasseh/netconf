package netconf

import (
	"errors"
	"testing"

	"github.com/carlmjohnson/be"
)

func TestReplyTable(t *testing.T) {
	newReq := func() *pendingReply { return &pendingReply{msg: make(chan *Message, 1)} }

	// resolved returns the operation a reply with msgID answers
	resolved := func(table *replyTable, msgID string) *pendingReply {
		req, _ := table.resolve(msgID)
		return req
	}

	t.Run("resolve by message-id", func(t *testing.T) {
		table := newReplyTable()
		a, b := newReq(), newReq()
		be.NilErr(t, table.register("1", a, false))
		be.NilErr(t, table.register("2", b, false))

		be.True(t, resolved(table, "2") == b)
		be.True(t, resolved(table, "2") == nil) // only once
		be.True(t, resolved(table, "1") == a)
	})

	t.Run("duplicate message-id", func(t *testing.T) {
		table := newReplyTable()
		be.NilErr(t, table.register("1", newReq(), false))
		be.Nonzero(t, table.register("1", newReq(), false))
	})

	t.Run("closing refuses all but internal operations", func(t *testing.T) {
		table := newReplyTable()
		table.close()
		be.True(t, errors.Is(table.register("1", newReq(), false), ErrClosed))
		be.NilErr(t, table.register("2", newReq(), true))
	})

	t.Run("forget does not drop a reused message-id", func(t *testing.T) {
		table := newReplyTable()
		a, b := newReq(), newReq()
		be.NilErr(t, table.register("1", a, false))
		be.True(t, resolved(table, "1") == a)
		be.NilErr(t, table.register("1", b, false))
		table.forget("1", a)
		be.True(t, resolved(table, "1") == b)
	})

	t.Run("reply without message-id answers the only operation", func(t *testing.T) {
		table := newReplyTable()
		a := newReq()
		be.NilErr(t, table.register("1", a, false))
		be.True(t, resolved(table, "") == a)
	})

	t.Run("reply without message-id is ambiguous with two operations", func(t *testing.T) {
		table := newReplyTable()
		be.NilErr(t, table.register("1", newReq(), false))
		be.NilErr(t, table.register("2", newReq(), false))
		be.True(t, resolved(table, "") == nil)
	})

	t.Run("reply without message-id may belong to an abandoned operation", func(t *testing.T) {
		table := newReplyTable()

		// A was sent and given up on; its answer is still out there.
		a := newReq()
		be.NilErr(t, table.register("1", a, false))
		table.markSent(a)
		table.forget("1", a)

		b := newReq()
		be.NilErr(t, table.register("2", b, false))
		be.True(t, resolved(table, "") == nil) // A's late, id-less error

		// With A accounted for, an id-less reply can only be B's.
		be.True(t, resolved(table, "") == b)
	})

	t.Run("operation that was never sent leaves no orphan", func(t *testing.T) {
		table := newReplyTable()
		a := newReq()
		be.NilErr(t, table.register("1", a, false))
		table.forget("1", a)

		b := newReq()
		be.NilErr(t, table.register("2", b, false))
		be.True(t, resolved(table, "") == b)
	})

	// The answer to an abandoned operation is expected, once.  Anything
	// beyond that is a reply nobody asked for.
	t.Run("late replies are told from unexpected ones", func(t *testing.T) {
		table := newReplyTable()
		a := newReq()
		be.NilErr(t, table.register("1", a, false))
		table.markSent(a)
		table.forget("1", a)

		req, late := table.resolve("1")
		be.True(t, req == nil)
		be.True(t, late)

		req, late = table.resolve("1")
		be.True(t, req == nil)
		be.False(t, late)
	})

	// A message the framing refused never went out.  Its caller may have
	// left before that was known, and must not leave an orphan behind: no
	// reply will come to settle it.
	t.Run("a refused operation is no orphan, whoever notices first", func(t *testing.T) {
		for _, forgetFirst := range []bool{true, false} {
			table := newReplyTable()
			a := newReq()
			be.NilErr(t, table.register("1", a, false))
			table.markSent(a)

			if forgetFirst {
				table.forget("1", a)
				table.notSent(a)
			} else {
				table.notSent(a)
				table.forget("1", a)
			}

			b := newReq()
			be.NilErr(t, table.register("2", b, false))
			be.True(t, resolved(table, "") == b)
		}
	})
}
