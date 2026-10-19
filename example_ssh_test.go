package netconf_test

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"os/exec"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/junos"
	"github.com/lasseh/netconf/rpc"
	nccmd "github.com/lasseh/netconf/transport/command"
	ncssh "github.com/lasseh/netconf/transport/ssh"
)

func Example_ssh() {
	// The context bounds connecting and opening the session.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Host keys are verified against ~/.ssh/known_hosts.
	transport, err := ncssh.DialHost(ctx, "myrouter.example.com",
		ncssh.WithUser("admin"),
		ncssh.WithPrivateKeyFile("/home/admin/.ssh/id_ed25519", ""),
	)
	if err != nil {
		panic(err)
	}

	session, err := netconf.Open(ctx, transport)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := session.Close(context.Background()); err != nil {
			log.Print(err)
		}
	}()

	// timeout for the call itself.
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deviceConfig, err := rpc.GetConfig{Source: rpc.Running}.Exec(ctx, session)
	if err != nil {
		panic(err)
	}

	log.Printf("Config:\n%s\n", deviceConfig)
}

// A ready-made ssh.ClientConfig can be used with Dial when DialHost's options
// are not enough.
func Example_sshClientConfig() {
	config := &ssh.ClientConfig{
		User:            "admin",
		Auth:            []ssh.AuthMethod{ssh.Password("secret")},
		HostKeyCallback: ssh.FixedHostKey(nil), // the device's host key
	}

	ctx := context.Background()
	transport, err := ncssh.Dial(ctx, "tcp", "myrouter.example.com:830", config)
	if err != nil {
		panic(err)
	}

	session, err := netconf.Open(ctx, transport)
	if err != nil {
		panic(err)
	}
	_ = session.Close(context.Background())
}

// Vendor RPCs without a typed operation are sent as literal XML.
func ExampleSession_Exec_rawRPC() {
	var session *netconf.Session // from netconf.Open

	var info struct {
		Hostname string `xml:"system-information>host-name"`
		Model    string `xml:"system-information>hardware-model"`
	}
	err := session.Exec(context.Background(), "<get-system-information/>", &info)

	var rpcErr netconf.RPCError
	switch {
	case errors.As(err, &rpcErr):
		log.Printf("device rejected the rpc: %s", rpcErr.Message)
	case err != nil:
		log.Printf("rpc failed: %v", err)
	default:
		log.Printf("%s is a %s", info.Hostname, info.Model)
	}
}

// A long-running program notices a dead session through Done.
func ExampleSession_Done() {
	ctx := context.Background()

	transport, err := ncssh.DialHost(ctx, "myrouter.example.com",
		ncssh.WithKeepalive(15*time.Second, 3),
	)
	if err != nil {
		panic(err)
	}

	session, err := netconf.Open(ctx, transport)
	if err != nil {
		panic(err)
	}

	go func() {
		<-session.Done()
		if err := session.Err(); err != nil {
			log.Printf("session lost, reconnect: %v", err)
		}
	}()

	version, err := junos.Command("show version").Exec(ctx, session)
	if err != nil {
		panic(err)
	}
	log.Print(version)
}

// A notification handler that issues operations on its own session.  The
// handler is given to Open, before there is a session, so it loads the
// session from where the program stores it once Open has returned.
// Notifications come only after the subscription, and by then it is there.
func ExampleWithNotifHandler() {
	ctx := context.Background()

	transport, err := ncssh.DialHost(ctx, "myrouter.example.com")
	if err != nil {
		panic(err)
	}

	var current atomic.Pointer[netconf.Session]
	session, err := netconf.Open(ctx, transport,
		netconf.WithNotifHandler(func(ctx context.Context, msg *netconf.Message) {
			var notif netconf.Notification
			if err := msg.Decode(&notif); err != nil {
				log.Print(err)
				return
			}
			if notif.Content.XMLName.Local != "netconf-config-change" {
				return
			}
			config, err := rpc.GetConfig{Source: rpc.Running}.Exec(ctx, current.Load())
			if err != nil {
				log.Print(err)
				return
			}
			log.Printf("the configuration is %d bytes now", len(config))
		}),
	)
	if err != nil {
		panic(err)
	}
	current.Store(session)

	// operations on a session with a subscription need :interleave, and on
	// Junos `notification interleave` in its configuration as well
	if !session.ServerCaps().Has(":interleave") {
		panic("the device cannot take operations on a session with a subscription")
	}
	if err := (rpc.CreateSubscription{}).Exec(ctx, session); err != nil {
		panic(err)
	}
	<-session.Done()
}

// A bound for the operations of a session, and more time for the one that
// needs it: a context with a deadline is taken at its word.
func ExampleWithOperationTimeout() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tr, err := ncssh.DialHost(ctx, "myrouter.example.com", ncssh.WithUser("admin"))
	if err != nil {
		panic(err)
	}
	session, err := netconf.Open(ctx, tr, netconf.WithOperationTimeout(30*time.Second))
	if err != nil {
		panic(err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	// no deadline: the 30 seconds of the session
	if _, err := (junos.GetSystemInformation{}).Exec(context.Background(), session); err != nil {
		log.Print(err)
	}

	// a commit can take longer than that
	commitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := junos.Change{Format: junos.FormatSet, Config: "set system ntp server 192.0.2.1"}.Apply(commitCtx, session)
	if err != nil {
		log.Print(err)
	}
	log.Print(result.Diff)
}

// A session that ends when its device stops in the middle of a message.
// The time has to be longer than the slowest operation takes: Junos begins
// a reply at once and finishes it when the work is done.
func ExampleWithMessageTimeout() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tr, err := ncssh.DialHost(ctx, "myrouter.example.com", ncssh.WithUser("admin"))
	if err != nil {
		panic(err)
	}
	session, err := netconf.Open(ctx, tr, netconf.WithMessageTimeout(10*time.Minute))
	if err != nil {
		panic(err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	<-session.Done()
	if errors.Is(session.Err(), netconf.ErrMessageStalled) {
		log.Print("the device stopped in the middle of a message")
	}
}

// A log line for every operation of a session, with how long it took and
// what became of it.  That covers the operations a junos.Change issues, the
// lock, the load, the commit and the rest, each under its own name.
func ExampleWithTrace() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	trace := &netconf.Trace{
		OperationDone: func(op netconf.OperationInfo, result netconf.OperationResult) {
			slog.Info("netconf operation",
				"session", op.SessionID,
				"operation", op.Name,
				"duration", result.Duration,
				"reply_bytes", result.ReplySize,
				"error", result.Err)
		},
		Notification: func(size int, dropped bool) {
			if dropped {
				slog.Warn("netconf notification dropped", "bytes", size)
			}
		},
	}

	tr, err := ncssh.DialHost(ctx, "myrouter.example.com", ncssh.WithUser("admin"))
	if err != nil {
		panic(err)
	}
	session, err := netconf.Open(ctx, tr, netconf.WithTrace(trace))
	if err != nil {
		panic(err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	if _, err := (junos.Change{Format: junos.FormatSet, Config: "set system ntp server 192.0.2.1"}).Apply(ctx, session); err != nil {
		slog.Error("change", "error", err)
	}
}

// A session over the ssh of the system, for what it can do and the ssh
// client of this library cannot: ProxyCommand, connection sharing, a key on
// a hardware token.  The login has to work without typing.
func Example_command() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// not exec.CommandContext with ctx: that would kill ssh when ctx ends,
	// with the session still in use
	tr, err := nccmd.Start(exec.Command("ssh", "-s", "-p", "830", "router1.example.net", "netconf")) //nolint:noctx // see above
	if err != nil {
		log.Print(err)
		return
	}
	session, err := netconf.Open(ctx, tr)
	if err != nil {
		log.Print(err) // has what ssh said when it gave up
		return
	}
	_ = session.Close(ctx)
}
