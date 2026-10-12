package netconf_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/junos"
	ncssh "github.com/lasseh/netconf/transport/ssh"
)

// The attributes of an element in a reply.  Junos says on <configuration>
// when it was committed.
func ExampleElement() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	reply, err := session.ExecReply(ctx, junos.GetConfiguration{})
	if err != nil {
		log.Print(err)
		return
	}
	config, err := reply.Element("configuration")
	if err != nil {
		log.Print(err)
		return
	}
	for _, attr := range config.Attr {
		// written junos:commit-seconds, with the seconds since 1970
		if attr.Name.Local == "commit-seconds" {
			seconds, _ := strconv.ParseInt(attr.Value, 10, 64)
			fmt.Println("committed", time.Unix(seconds, 0))
		}
	}
}

// A reply with a character XML does not allow says where it is.  The bytes
// around it are in the error for who prints them, and not in its text.
func ExampleSyntaxError() {
	reply := &netconf.Reply{Raw: []byte("<rpc-reply><description>Vendor OS 1.2\x01build 7</description></rpc-reply>")}

	_, err := reply.Element("description")

	var syntaxErr *netconf.SyntaxError
	if errors.As(err, &syntaxErr) {
		fmt.Printf("byte %d of the reply: %q\n", syntaxErr.Offset, syntaxErr.Around)
	}
	fmt.Println(err)

	// Output:
	// byte 37 of the reply: "reply><description>Vendor OS 1.2\x01build 7</description></rpc-repl"
	// failed to parse <description>: XML syntax error on line 1: illegal character code U+0001 at byte 37
}

// A session that reads replies with characters XML does not allow in them.
func ExampleWithReplyRepair() {
	ctx := context.Background()

	tr, err := ncssh.DialHost(ctx, "myrouter.example.com", ncssh.WithUser("admin"))
	if err != nil {
		log.Print(err)
		return
	}
	session, err := netconf.Open(ctx, tr, netconf.WithReplyRepair(netconf.StripInvalidChars))
	if err != nil {
		log.Print(err)
		return
	}
	defer func() { _ = session.Close(context.Background()) }()

	reply, err := session.ExecReply(ctx, "<get-lldp-neighbors-information/>")
	if err != nil {
		log.Print(err)
		return
	}
	if _, err := reply.Element("lldp-neighbors-information"); err != nil {
		log.Print(err)
	}
}
