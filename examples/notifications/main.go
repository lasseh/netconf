// Command notifications subscribes on a dedicated session and prints event metadata.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/examples/internal/exampleutil"
	"github.com/lasseh/netconf/rpc"
)

func main() {
	handler := func(_ context.Context, msg *netconf.Message) {
		var notification netconf.Notification
		if err := msg.Decode(&notification); err != nil {
			log.Printf("decode notification: %v", err)
			return
		}
		// Do not print the payload: it may contain configuration secrets.
		log.Printf("event=%s time=%s", notification.Content.XMLName.Local, notification.EventTime)
	}
	exampleutil.Main(func(ctx context.Context, session *netconf.Session) error {
		if !session.ServerCaps().Has(":notification:1.0") {
			return errors.New("device does not advertise notifications")
		}
		if err := (rpc.CreateSubscription{}).Exec(ctx, session); err != nil {
			return fmt.Errorf("subscribe: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-session.Done():
			if err := session.Err(); err != nil {
				return fmt.Errorf("subscription ended; events may have been lost: %w", err)
			}
			return errors.New("subscription session ended")
		}
	}, netconf.WithNotifHandler(handler))
}
