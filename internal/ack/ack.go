// Package ack holds a device to the <ok/> with which it says that it carried
// out an operation, for the typed operations of the rpc and junos packages.
package ack

import (
	"fmt"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/operr"
)

// Check returns nil when reply holds an <ok/>, wherever it is in the reply:
// RFC6241 puts it directly under <rpc-reply>, Junos nests it in
// <load-configuration-results>.  name is the name of the operation, which
// every error begins with.
//
// A reply without it is not always an error to Junos: a commit from a private
// copy that no longer fits the configuration, because somebody else committed
// in the meantime, is answered with a warning for each statement and a count
// of them, and commits nothing.
//
// The count is what makes such a reply a refusal.  The error then holds the
// warnings as netconf.RPCErrors, like the error of an operation that Junos
// rejects.  A reply with warnings and no count has not been seen from a
// device and says nothing certain, so its error holds their text only.
//
// The reply is read to its end for the <ok/>.  One that stops being XML
// behind it says nothing certain either, and fails with where it stops.
func Check(reply *netconf.Reply, name string) error {
	oks, err := reply.Elements("ok")
	switch {
	case err != nil:
		return operr.Named(name, err)
	case len(oks) > 0:
		return nil
	}
	err = fmt.Errorf("%w: <ok>", netconf.ErrElementNotFound)
	_, counted := reply.Element("load-error-count")
	switch {
	case len(reply.Warnings) == 0:
	case counted == nil:
		err = fmt.Errorf("%w: %w", err, reply.Warnings)
	default:
		// the text, not the errors: this is no refusal
		err = fmt.Errorf("%w: %s", err, reply.Warnings.Error())
	}
	return operr.Named(name, err)
}
