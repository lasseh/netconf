package netconf_test

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net"
	"os/exec"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

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

// Telling the ways apart in which connecting fails.
func ExampleDialHost_failures() {
	ctx := context.Background()

	tr, err := ncssh.DialHost(ctx, "myrouter.example.com", ncssh.WithUser("automation"))

	var (
		keyErr *knownhosts.KeyError
		algErr *ssh.AlgorithmNegotiationError
	)
	switch {
	case err == nil:
	case errors.Is(err, ncssh.ErrAuth):
		log.Fatalf("the device, or a jump host, did not accept the login: %v", err)
	case errors.Is(err, ncssh.ErrSubsystemRefused):
		log.Fatalf("logged in, and no NETCONF there: %v", err)
	case errors.As(err, &keyErr) && len(keyErr.Want) == 0:
		log.Fatalf("the host is not among the known hosts: %v", err)
	case errors.As(err, &keyErr):
		log.Fatalf("the host has another key than the known one, or than the one given: %v", err)
	case errors.As(err, &algErr):
		log.Fatalf("no algorithm in common, see WithClientConfig: %v", err)
	case errors.Is(err, context.DeadlineExceeded):
		log.Fatalf("out of time: %v", err)
	default:
		log.Fatalf("the network: %v", err)
	}

	// the hello exchange can fail after all of that went well
	session, err := netconf.Open(ctx, tr)

	var helloErr *netconf.HelloError
	switch {
	case errors.As(err, &helloErr):
		log.Printf("no session: %v", helloErr.Err)
		for i := range helloErr.RPCErrors {
			log.Printf("the device said: %s", helloErr.RPCErrors[i].Message)
		}
	case err != nil:
		log.Print(err)
	default:
		_ = session.Close(ctx)
	}
}

// The host key of a device from an inventory, for a program that has no
// known_hosts file.
func ExampleWithHostKey() {
	ctx := context.Background()

	// a line as in authorized_keys or as ssh-keyscan prints it, without the
	// host in front
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKeyOfTheDeviceFromTheInventory"))
	if err != nil {
		log.Print(err)
		return
	}

	tr, err := ncssh.DialHost(ctx, "switch1.example.net",
		ncssh.WithUser("automation"),
		ncssh.WithHostKey(hostKey),
	)
	if err != nil {
		log.Print(err)
		return
	}
	_ = tr.Close()
}

// A private key that comes from a vault and never was a file, and the host
// key of the device from an inventory: a login that needs nothing on disk.
func ExampleWithSigner() {
	ctx := context.Background()
	var pemFromTheVault, hostKeyFromTheInventory []byte

	signer, err := ssh.ParsePrivateKey(pemFromTheVault)
	if err != nil {
		log.Print(err)
		return
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostKeyFromTheInventory)
	if err != nil {
		log.Print(err)
		return
	}

	tr, err := ncssh.DialHost(ctx, "switch1.example.net",
		ncssh.WithUser("automation"),
		ncssh.WithSigner(signer),
		ncssh.WithHostKey(hostKey),
	)
	if err != nil {
		log.Print(err)
		return
	}
	_ = tr.Close()
}

// A device that opens the connection itself, because it sits behind NAT or
// a firewall: the program listens, and runs the dial on what it accepts.
// The name given to DialHost is the one the device is known under; its host
// key is checked against that entry of the known hosts, so it does not
// matter where the connection came from.
func ExampleWithDialer_listener() {
	ctx := context.Background()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", ":7804")
	if err != nil {
		log.Print(err)
		return
	}
	defer func() { _ = ln.Close() }()

	conn, err := ln.Accept()
	if err != nil {
		log.Print(err)
		return
	}

	// which device this is has to be known from somewhere: from what it
	// sends first, or from the address it has
	tr, err := ncssh.DialHost(ctx, "switch1.example.net",
		ncssh.WithUser("automation"),
		ncssh.WithDialer(func(context.Context, string, string) (net.Conn, error) { return conn, nil }),
	)
	if err != nil {
		// DialHost has closed a connection it took.  One it never got to,
		// as it failed over a key file or the known hosts, is still open.
		_ = conn.Close()
		log.Print(err)
		return
	}

	session, err := netconf.Open(ctx, tr)
	if err != nil {
		log.Print(err)
		return
	}
	_ = session.Close(ctx)
}

// A connection from an address of the program's choosing, on a host with
// several.
func ExampleWithDialer_sourceAddress() {
	ctx := context.Background()

	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("192.0.2.10")}}
	tr, err := ncssh.DialHost(ctx, "switch1.example.net",
		ncssh.WithUser("automation"),
		ncssh.WithDialer(dialer.DialContext),
	)
	if err != nil {
		log.Print(err)
		return
	}
	_ = tr.Close()
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

// A Junos device that has no NETCONF service configured: its ssh service
// refuses the netconf subsystem and starts NETCONF with a command.
func ExampleWithCommand() {
	ctx := context.Background()

	tr, err := ncssh.DialHost(ctx, "router.example.com",
		ncssh.WithUser("admin"),
		ncssh.WithPort(22),
		ncssh.WithCommand("xml-mode netconf need-trailer"),
	)
	if err != nil {
		log.Print(err)
		return
	}

	// The command asks for the end-of-message trailer, and Junos keeps to
	// it where its hello says base:1.1, so the session offers base:1.0.
	session, err := netconf.Open(ctx, tr, netconf.WithCapability(netconf.CapNetConf10))
	if err != nil {
		_ = tr.Close()
		log.Print(err)
		return
	}
	_ = session.Close(ctx)
}
