package rpc_test

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/rpc"
	"github.com/lasseh/netconf/transport"
)

// A part of the running configuration, picked with a subtree filter and
// decoded into a struct.
func ExampleGetConfig() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	config, err := rpc.GetConfig{
		Source: rpc.Running,
		Filter: rpc.SubtreeFilter(`<interfaces xmlns="urn:ietf:params:xml:ns:yang:ietf-interfaces"/>`),
	}.Exec(ctx, session)
	if err != nil {
		log.Fatal(err)
	}

	// config is what the device put in <data>
	var interfaces struct {
		Names []string `xml:"interface>name"`
	}
	if err := xml.Unmarshal(config, &interfaces); err != nil {
		log.Fatal(err)
	}
	fmt.Println(interfaces.Names)
}

// A change the standard way: lock the candidate, edit it, have the device
// check it, commit, and give the candidate back as it was when any of that
// fails.
func ExampleEditConfig() {
	var session *netconf.Session // from netconf.Open
	ctx := context.Background()

	change := func() error {
		if err := (rpc.Lock{Target: rpc.Candidate}).Exec(ctx, session); err != nil {
			if errors.Is(err, netconf.ErrLockDenied) {
				return fmt.Errorf("somebody else has the candidate: %w", err)
			}
			return err
		}
		defer func() {
			if err := (rpc.Unlock{Target: rpc.Candidate}).Exec(ctx, session); err != nil {
				log.Printf("the candidate is still locked: %v", err)
			}
		}()

		err := rpc.EditConfig{
			Target: rpc.Candidate,
			Config: `<system xmlns="urn:ietf:params:xml:ns:yang:ietf-system"><hostname>router1</hostname></system>`,
		}.Exec(ctx, session)
		if err == nil {
			err = rpc.Validate{Source: rpc.Candidate}.Exec(ctx, session)
		}
		if err == nil {
			err = rpc.Commit{}.Exec(ctx, session)
		}
		if err != nil {
			// what was edited is not to stay behind for whoever locks next
			if discard := (rpc.DiscardChanges{}).Exec(ctx, session); discard != nil {
				err = errors.Join(err, discard)
			}
		}
		return err
	}

	if err := change(); err != nil {
		log.Fatal(err)
	}
}

// Notifications.  The handler is given when the session is opened and gets
// every notification once a subscription exists.
func ExampleCreateSubscription() {
	var tr transport.Transport // from ncssh.DialHost
	ctx := context.Background()

	session, err := netconf.Open(ctx, tr, netconf.WithNotifHandler(func(_ context.Context, msg *netconf.Message) {
		var notif netconf.Notification
		if err := msg.Decode(&notif); err != nil {
			log.Print(err)
			return
		}

		// The content is one element, whose name says what happened.  This
		// one is the system log event that Junos 26.2 sends for everything.
		if notif.Content.XMLName.Local != "syslog" {
			return
		}
		var event struct {
			ID      string `xml:"junos>events>event>id"`
			Message string `xml:"junos>events>event>message"`
		}
		if err := notif.Content.Decode(&event); err != nil {
			log.Print(err)
			return
		}
		if event.ID == "UI_COMMIT" {
			fmt.Println(notif.EventTime, event.Message)
		}
	}))
	if err != nil {
		log.Fatal(err)
	}

	if err := (rpc.CreateSubscription{}).Exec(ctx, session); err != nil {
		log.Fatal(err)
	}
	<-session.Done()
}
