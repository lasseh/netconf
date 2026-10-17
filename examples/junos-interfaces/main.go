// Command junos-interfaces reads interface names and operational status from Junos.
package main

import (
	"context"
	"fmt"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/examples/internal/exampleutil"
	"github.com/lasseh/netconf/junos"
)

type physicalInterface struct {
	Name string `xml:"name"`
	Oper string `xml:"oper-status"`
}

func interfaces(reply *netconf.Reply) ([]physicalInterface, error) {
	var result []physicalInterface
	if err := junos.Decode(reply, "physical-interface", &result); err != nil {
		return nil, fmt.Errorf("decode interfaces: %w", err)
	}
	return result, nil
}

func main() {
	exampleutil.Main(func(ctx context.Context, session *netconf.Session) error {
		reply, err := session.ExecReply(ctx, "<get-interface-information><terse/></get-interface-information>")
		if err != nil {
			return fmt.Errorf("read interfaces: %w", err)
		}
		result, err := interfaces(reply)
		if err != nil {
			return err
		}
		for _, iface := range result {
			if _, err := fmt.Printf("%s\t%s\n", iface.Name, iface.Oper); err != nil {
				return fmt.Errorf("write interface: %w", err)
			}
		}
		return nil
	})
}
