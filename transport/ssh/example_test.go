package ssh_test

import (
	"context"
	"errors"
	"log"
	"net"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/lasseh/netconf"
	ncssh "github.com/lasseh/netconf/transport/ssh"
)

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
