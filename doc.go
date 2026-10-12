// Package netconf is a client for the NETCONF network management protocol
// (RFC 6241).
//
// A [Session] runs over a [transport.Transport], which the transport/ssh and
// transport/tls packages provide.  [Open] performs the hello exchange:
//
//	tr, err := ncssh.DialHost(ctx, "router1.example.net")
//	if err != nil { ... }
//	session, err := netconf.Open(ctx, tr)
//	if err != nil { ... }
//	defer session.Close(context.Background())
//
// # Operations
//
// The rpc package has the standard operations (get-config, edit-config,
// lock, commit, ...) and the junos package the Junos-native ones.  Each is a
// struct with an Exec method:
//
//	cfg, err := rpc.GetConfig{Source: rpc.Running}.Exec(ctx, session)
//
// Any other operation can be sent as literal XML, or as a value that
// marshals to it, with [Session.ExecReply] for the whole reply or
// [Session.Exec] to decode it:
//
//	reply, err := session.ExecReply(ctx, "<get-system-information/>")
//
// [Reply.Element] finds a result by name wherever the device put it in the
// reply.  [Session.Stream] is for replies too large to hold in memory.
//
// # Errors
//
// An rpc-error in a reply is returned as [RPCErrors] by Exec and ExecReply,
// wherever in the reply the device put it.  So is the error element Junos
// answers some requests with in its place, see [RPCError].  Errors of
// warning severity do not fail an operation; they are in [Reply.Warnings].
// An operation on a session that is over fails with [ErrClosed], wrapping
// the reason.
//
// The operations of the packages rpc and junos put their name in front of
// what the session returns, "lock: netconf error: protocol lock-denied: ...",
// so their errors are looked into with errors.Is and errors.As and not with
// a type assertion.
//
// # Concurrency and cancellation
//
// A session is safe for concurrent use.  Every operation takes a context:
// when it fires the operation is abandoned and its reply dropped, while the
// session stays usable.  The device may still carry the operation out.
// [WithOperationTimeout] bounds every operation, for callers that bring no
// deadline of their own.
//
// Operations issued at the same time are written one behind the other as
// they come.  Over chunked framing to a Junos device each is written when
// the one before it has been answered: Junos 26.2 ended sessions over a
// request that was written right behind another.  An operation whose
// context fires while it waits there is not sent.
//
// [Session.Done] is closed when the session ends, by [Session.Close] or
// because the transport died, and [Session.Err] reports why.
//
// # Notifications
//
// Notifications (RFC 5277) are delivered to the handler set with
// [WithNotifHandler], one at a time in the order received, once a
// subscription was created with rpc.CreateSubscription.  What waits for a
// busy handler is bounded, see [WithNotifQueue].
//
// # Text from Junos
//
// Junos keeps text as bytes and sends every byte above 127 as a character
// reference of its own: "ø", in UTF-8 the bytes C3 B8, arrives as
// "&#195;&#184;", which an XML parser reads as the two characters "Ã¸".  For
// a device that advertises the Junos capability the session puts such
// references back together, in every reply and notification it buffers:
// [Reply.Raw] and all that is read from it have the character.  A reply
// read with [Session.Stream] is as the device sent it.
//
// A reference that is not part of a character in UTF-8 stays as it is.
// That reads right too: text that Junos holds in Latin-1 arrives as
// "&#248;", the character itself.  Only when two characters of such a text
// are UTF-8 as bytes, an "Ã" followed by a "¸", are they taken for the one
// character.
//
// # Untrusted devices
//
// A message larger than [WithMaxMessageSize] ends the session, and so do a
// device that keeps sending messages nobody asked for and notifications
// arriving faster than the handler takes them.
package netconf
