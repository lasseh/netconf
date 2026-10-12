package junos

import "github.com/lasseh/netconf"

// Junos has no error tag for some of the things a program wants to tell
// apart: every one of them is "operation-failed", and the message says
// which.  The targets below read the message, as Junos 26.2 and Junos OS
// Evolved 26.2 word it, in the default mode and with rfc-compliant set.
// Look for them with errors.Is, on the error of any operation.
var (
	// ErrPermissionDenied says that the login class of the user does not
	// permit what was asked for: a request, a word of a command, a part of
	// the configuration.  Detail("bad-element") of the netconf.RPCError
	// names it.  A read of configuration the class may not see fails with
	// it, with an error for every such part, and so does a load, in which
	// Junos first calls the command of a set line unknown.
	//
	// Junos does not send netconf.ErrAccessDenied for it.
	ErrPermissionDenied error = &refusal{"junos: permission denied", func(e netconf.RPCError) bool {
		return e.Message == "permission denied"
	}}

	// ErrUnsupported says that Junos does not know the request: this
	// platform or this release has none of that name, as an SRX request on
	// a router.  Junos answers with a syntax error that names the request
	// as the bad element.
	//
	// A load of a statement Junos does not know is the same error with the
	// statement as the bad element, which is not the request.  A request
	// that Junos knows and has no use for on the platform does not match
	// either: it is refused in words of its own, "command is not valid on
	// the vmx".  Neither does a CLI line given to Command that is no
	// command: every Junos has the request for that.
	//
	// It needs the request the error answers, netconf.RPCError.Request,
	// which the errors of Exec and ExecReply have.
	ErrUnsupported error = &refusal{"junos: request not known to the device", func(e netconf.RPCError) bool {
		bad, _ := e.Detail("bad-element")
		return e.Message == "syntax error" && bad != "" && bad == e.Request() && bad != "command"
	}}
)

// refusal is a target for errors.Is that matches an RPC error by what Junos
// wrote in it.
type refusal struct {
	text  string
	match func(netconf.RPCError) bool
}

func (r *refusal) Error() string { return r.text }

// MatchRPCError is what netconf.RPCError asks a target that is no tag.
func (r *refusal) MatchRPCError(e netconf.RPCError) bool { return r.match(e) }
