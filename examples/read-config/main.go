// Command read-config reads running configuration, optionally with a subtree filter.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/examples/internal/exampleutil"
	"github.com/lasseh/netconf/rpc"
)

func main() {
	filterFile := flag.String("filter", "", "optional file containing a subtree filter in the device's data model")
	exampleutil.Main(func(ctx context.Context, session *netconf.Session) error {
		op := rpc.GetConfig{Source: rpc.Running}
		if *filterFile != "" {
			filter, err := os.ReadFile(*filterFile)
			if err != nil {
				return fmt.Errorf("read filter: %w", err)
			}
			op.Filter = rpc.SubtreeFilter(filter)
		}
		config, err := op.Exec(ctx, session)
		if err != nil {
			return fmt.Errorf("read running configuration: %w", err)
		}
		if _, err := os.Stdout.Write(config); err != nil {
			return fmt.Errorf("write configuration: %w", err)
		}
		return nil
	})
}
