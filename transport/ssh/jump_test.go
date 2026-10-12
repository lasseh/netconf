package ssh

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// jumpHost is what a test sees of the jump host serveJump started.
type jumpHost struct {
	// dests are the addresses the jump host was asked to connect to
	dests chan string
	// gone is closed once the connection from the client has ended
	gone chan struct{}
}

// serveJump serves one connection on srv as a jump host: every direct-tcpip
// channel is connected to the address it asks for.
func serveJump(srv *testServer) *jumpHost {
	j := &jumpHost{dests: make(chan string, 8), gone: make(chan struct{})}
	srv.t.Cleanup(func() { _ = srv.listener.Close() })

	go func() {
		defer close(j.gone)

		conn, err := srv.listener.Accept()
		_ = srv.listener.Close()
		if err != nil {
			return
		}
		sc, chans, reqs, err := ssh.NewServerConn(conn, srv.config)
		if err != nil {
			return
		}
		defer func() { _ = sc.Close() }()
		go ssh.DiscardRequests(reqs)

		for newChannel := range chans {
			if newChannel.ChannelType() != "direct-tcpip" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "a jump host forwards and nothing else")
				continue
			}
			srv.forward(newChannel, j.dests)
		}
	}()
	return j
}

// forward connects a direct-tcpip channel to the address it names.
func (s *testServer) forward(newChannel ssh.NewChannel, dests chan<- string) {
	var req struct {
		Raddr string
		Rport uint32
		Laddr string
		Lport uint32
	}
	if err := ssh.Unmarshal(newChannel.ExtraData(), &req); err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	dest := net.JoinHostPort(req.Raddr, strconv.Itoa(int(req.Rport)))
	dests <- dest

	target, err := (&net.Dialer{}).DialContext(s.t.Context(), "tcp", dest)
	if err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := newChannel.Accept()
	if err != nil {
		_ = target.Close()
		return
	}
	go ssh.DiscardRequests(reqs)

	// either side ending ends both
	go func() {
		_, _ = io.Copy(target, ch)
		_ = target.Close()
		_ = ch.Close()
	}()
	go func() {
		_, _ = io.Copy(ch, target)
		_ = ch.Close()
		_ = target.Close()
	}()
}

// sshHome gives the test a home directory of its own with one default
// identity in it, and no agent, so that DialHost finds nothing of whoever
// runs the tests.  It returns the directory and the identity's public key.
func sshHome(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")

	keyPath, pub := writeKey(t, "")
	key, err := os.ReadFile(keyPath)
	be.NilErr(t, err)
	be.NilErr(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	be.NilErr(t, os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), key, 0o600))
	return home, pub
}

// trust adds the host key of the server at addr to the known hosts in home.
func trust(t *testing.T, home, addr string, key ssh.PublicKey) {
	t.Helper()

	f, err := os.OpenFile(filepath.Join(home, ".ssh", "known_hosts"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	be.NilErr(t, err)
	_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(addr)}, key))
	be.NilErr(t, err)
	be.NilErr(t, f.Close())
}

// newJump starts a jump host that lets everybody in and is known in home.
func newJump(t *testing.T, home string) (addr string, j *jumpHost) {
	t.Helper()

	srv := newTestServer(t)
	trust(t, home, srv.Addr(), srv.hostKey)
	return srv.Addr(), serveJump(srv)
}

func recvWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func TestParseProxyJump(t *testing.T) {
	tests := []struct {
		value string
		want  []jumpSpec
	}{
		{"", nil},
		{"none", nil},
		{"NONE", nil},
		{"bastion", []jumpSpec{{host: "bastion"}}},
		{"ops@bastion", []jumpSpec{{user: "ops", host: "bastion"}}},
		{"bastion:2222", []jumpSpec{{host: "bastion", port: 2222}}},
		{"ops@bastion:2222", []jumpSpec{{user: "ops", host: "bastion", port: 2222}}},
		{"a,ops@b:2222,c", []jumpSpec{{host: "a"}, {user: "ops", host: "b", port: 2222}, {host: "c"}}},
		{" a , b ", []jumpSpec{{host: "a"}, {host: "b"}}},
		{"2001:db8::1", []jumpSpec{{host: "2001:db8::1"}}},
		{"[2001:db8::1]", []jumpSpec{{host: "2001:db8::1"}}},
		{"ops@[2001:db8::1]:2222", []jumpSpec{{user: "ops", host: "2001:db8::1", port: 2222}}},
		{"ssh://bastion", []jumpSpec{{host: "bastion"}}},
		{"ssh://ops@bastion:2222", []jumpSpec{{user: "ops", host: "bastion", port: 2222}}},
		{"ssh://[2001:db8::1]:2222", []jumpSpec{{host: "2001:db8::1", port: 2222}}},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			got, err := parseProxyJump(tc.value)
			be.NilErr(t, err)
			be.AllEqual(t, tc.want, got)
		})
	}

	for _, value := range []string{
		"a,,b", ",a", "a,", "@bastion", "ops@", "bastion:", "bastion:ssh", "bastion:0", "bastion:65536",
		":22", "ssh://", "ssh://bastion/path",
	} {
		t.Run("invalid "+value, func(t *testing.T) {
			_, err := parseProxyJump(value)
			be.Nonzero(t, err)
		})
	}
}

func TestDialHost_ProxyJump(t *testing.T) {
	home, _ := sshHome(t)
	jumpAddr, jump := newJump(t, home)

	device := newTestServer(t)
	device.Serve(greet)
	host, port := hostPort(t, device.Addr())

	tr, err := DialHost(t.Context(), host,
		WithPort(port),
		WithPassword(""),
		WithInsecureIgnoreHostKey(),
		WithProxyJump(jumpAddr),
	)
	be.NilErr(t, err)
	be.Equal(t, device.Addr(), recvWithin(t, jump.dests, "the jump host to be asked for the device"))
	readGreeting(t, tr)

	// closing the transport takes down every connection on the way
	be.NilErr(t, tr.Close())
	recvWithin(t, jump.gone, "the jump host to see its connection end")
	be.NilErr(t, device.Wait(t))
}

// Jump hosts are logged in to in the order they are given, each through the
// one before it.
func TestDialHost_ProxyJumpChain(t *testing.T) {
	for _, joined := range []bool{false, true} {
		t.Run(fmt.Sprintf("joined=%t", joined), func(t *testing.T) {
			home, _ := sshHome(t)
			firstAddr, first := newJump(t, home)
			secondAddr, second := newJump(t, home)

			device := newTestServer(t)
			device.Serve(greet)
			host, port := hostPort(t, device.Addr())

			jumps := []string{firstAddr, secondAddr}
			if joined {
				jumps = []string{firstAddr + "," + secondAddr}
			}
			tr, err := DialHost(t.Context(), host,
				WithPort(port),
				WithPassword(""),
				WithInsecureIgnoreHostKey(),
				WithProxyJump(jumps...),
			)
			be.NilErr(t, err)
			be.Equal(t, secondAddr, recvWithin(t, first.dests, "the first jump host to be asked for the second"))
			be.Equal(t, device.Addr(), recvWithin(t, second.dests, "the second jump host to be asked for the device"))
			readGreeting(t, tr)

			be.NilErr(t, tr.Close())
			recvWithin(t, first.gone, "the first jump host to see its connection end")
			recvWithin(t, second.gone, "the second jump host to see its connection end")
			be.NilErr(t, device.Wait(t))
		})
	}
}

// The ssh config says how to reach a device and how to log in to the jump
// host: each of the two gets the user and the key of its own block.
func TestDialHost_ProxyJumpFromSSHConfig(t *testing.T) {
	// setup starts a device and a jump host and writes an ssh config in
	// which the device goes by three names: "switch1" through the jump host,
	// "direct" without one and "lost" through one nobody listens on.
	setup := func(t *testing.T) (config string, port int, jump *jumpHost) {
		t.Helper()
		home, _ := sshHome(t)

		jumpKey, jumpPub := writeKey(t, "")
		jumpSrv := newTestServer(t)
		requireKey(jumpSrv, "hopper", jumpPub)
		trust(t, home, jumpSrv.Addr(), jumpSrv.hostKey)
		jumpHostname, jumpPort := hostPort(t, jumpSrv.Addr())

		deviceKey, devicePub := writeKey(t, "")
		device := newTestServer(t)
		requireKey(device, "admin", devicePub)
		device.Serve(greet)
		deviceHostname, port := hostPort(t, device.Addr())

		config = filepath.Join(t.TempDir(), "config")
		be.NilErr(t, os.WriteFile(config, fmt.Appendf(nil, `
Host bastion
    HostName %s
    Port %d
    User hopper
    IdentityFile %s

Host switch1
    ProxyJump bastion

Host direct
    ProxyJump none

Host lost
    ProxyJump 127.0.0.1:1

Host scripted
    ProxyCommand nc %%h %%p

Host switch1 direct lost scripted
    HostName %s
    User admin
    IdentityFile %s
`, jumpHostname, jumpPort, jumpKey, deviceHostname, deviceKey), 0o600))
		return config, port, serveJump(jumpSrv)
	}

	tests := []struct {
		name string
		host string
		opts []DialOption
		// jumped says whether the device is reached through the jump host
		jumped bool
		// fails is part of the error of a dial that must not work
		fails string
	}{
		{name: "the jump host of the config", host: "switch1", jumped: true},
		{name: "none in the config", host: "direct"},
		{name: "a jump host nobody listens on", host: "lost", fails: "jump host 127.0.0.1"},
		{name: "the option instead of the config", host: "lost", opts: []DialOption{WithProxyJump("bastion")}, jumped: true},
		{name: "the option switches the config off", host: "switch1", opts: []DialOption{WithProxyJump("none")}},
		{name: "the option without jump hosts does too", host: "switch1", opts: []DialOption{WithProxyJump()}},
		{name: "a proxy command", host: "scripted", fails: "host scripted requires ProxyCommand"},
		{name: "the option instead of a proxy command", host: "scripted", opts: []DialOption{WithProxyJump("bastion")}, jumped: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config, port, jump := setup(t)

			opts := append([]DialOption{WithPort(port), WithSSHConfig(config), WithInsecureIgnoreHostKey()}, tc.opts...)
			tr, err := DialHost(t.Context(), tc.host, opts...)
			if tc.fails != "" {
				be.Nonzero(t, err)
				be.In(t, tc.fails, err.Error())
				return
			}
			be.NilErr(t, err)
			readGreeting(t, tr)
			be.Equal(t, tc.jumped, len(jump.dests) == 1)
			be.NilErr(t, tr.Close())
		})
	}
}

// A jump host that needs a jump host or a proxy command of its own is
// refused before anything is dialed.
func TestDialHost_ProxyJumpNotFollowed(t *testing.T) {
	tests := []struct {
		name  string
		block string
		// fails is what the error has to say.  The test's name is in its
		// paths, so the name of a directive alone proves nothing.
		fails string
	}{
		{
			name:  "a jump host of its own",
			block: "    ProxyJump 127.0.0.1:1\n",
			fails: "jump host bastion has a ProxyJump of its own (127.0.0.1:1)",
		},
		{
			name:  "a proxy command",
			block: "    ProxyCommand nc %h %p\n",
			fails: "host bastion requires ProxyCommand",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sshHome(t)
			config := filepath.Join(t.TempDir(), "config")
			be.NilErr(t, os.WriteFile(config, []byte("Host bastion\n    HostName 127.0.0.1\n"+tc.block+
				"\nHost switch1\n    HostName 127.0.0.1\n    ProxyJump bastion\n"), 0o600))

			_, err := DialHost(t.Context(), "switch1", WithSSHConfig(config), WithInsecureIgnoreHostKey())
			be.Nonzero(t, err)
			be.In(t, tc.fails, err.Error())
		})
	}

	// further down the list a jump host is reached through the one before
	// it, whatever the config says about it
	t.Run("only the first of a list", func(t *testing.T) {
		home, _ := sshHome(t)
		firstAddr, _ := newJump(t, home)
		secondSrv := newTestServer(t)
		trust(t, home, secondSrv.Addr(), secondSrv.hostKey)
		serveJump(secondSrv)
		secondHost, secondPort := hostPort(t, secondSrv.Addr())

		device := newTestServer(t)
		device.Serve(greet)
		host, port := hostPort(t, device.Addr())

		config := filepath.Join(t.TempDir(), "config")
		be.NilErr(t, os.WriteFile(config, fmt.Appendf(nil,
			"Host inner\n    HostName %s\n    Port %d\n    ProxyJump 127.0.0.1:1\n", secondHost, secondPort), 0o600))

		tr, err := DialHost(t.Context(), host, WithPort(port), WithPassword(""), WithSSHConfig(config),
			WithInsecureIgnoreHostKey(), WithProxyJump(firstAddr, "inner"))
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
	})
}

// offered is what a server was asked to check during authentication.
type offered struct {
	mu        sync.Mutex
	users     []string
	keys      [][]byte
	passwords int
}

func (o *offered) saw(key ssh.PublicKey) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, k := range o.keys {
		if bytes.Equal(k, key.Marshal()) {
			return true
		}
	}
	return false
}

// recordAuth makes srv note everything it is offered and let in only who
// has the key accept, nobody when that is nil.
func recordAuth(srv *testServer, accept ssh.PublicKey) *offered {
	o := &offered{}
	note := func(conn ssh.ConnMetadata) {
		o.users = append(o.users, conn.User())
	}

	srv.config.NoClientAuth = false
	srv.config.PublicKeyCallback = func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		o.mu.Lock()
		defer o.mu.Unlock()
		note(conn)
		o.keys = append(o.keys, key.Marshal())
		if accept == nil || !bytes.Equal(key.Marshal(), accept.Marshal()) {
			return nil, errors.New("key refused")
		}
		return nil, nil
	}
	srv.config.PasswordCallback = func(conn ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
		o.mu.Lock()
		defer o.mu.Unlock()
		note(conn)
		o.passwords++
		return nil, errors.New("password refused")
	}
	srv.config.KeyboardInteractiveCallback = func(conn ssh.ConnMetadata, _ ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		o.mu.Lock()
		defer o.mu.Unlock()
		note(conn)
		o.passwords++
		return nil, errors.New("keyboard-interactive refused")
	}
	return o
}

// What the caller says about the device is for the device.  A jump host
// never gets to see the device's password, key or user name: it is logged in
// to with the identity files and the agent, the way ssh does it.
func TestDialHost_ProxyJumpCredentials(t *testing.T) {
	osUser, err := user.Current()
	be.NilErr(t, err)

	// deviceOpts are credentials for the device, none of them a jump host's
	// business
	deviceKey, devicePub := writeKey(t, "")
	deviceOpts := func(port int, jumpAddr string) []DialOption {
		return []DialOption{
			WithPort(port),
			WithUser("admin"),
			WithPassword("device-secret"),
			WithPrivateKeyFile(deviceKey, ""),
			WithInsecureIgnoreHostKey(),
			WithProxyJump(jumpAddr),
		}
	}
	checkJump := func(t *testing.T, seen *offered) {
		t.Helper()
		seen.mu.Lock()
		passwords, users := seen.passwords, seen.users
		seen.mu.Unlock()

		be.Equal(t, 0, passwords)
		be.False(t, seen.saw(devicePub))
		be.Nonzero(t, len(users))
		for _, u := range users {
			be.Equal(t, osUser.Username, u)
		}
	}

	t.Run("a jump host that lets nobody in", func(t *testing.T) {
		home, homePub := sshHome(t)
		jumpSrv := newTestServer(t)
		trust(t, home, jumpSrv.Addr(), jumpSrv.hostKey)
		seen := recordAuth(jumpSrv, nil)
		jump := serveJump(jumpSrv)

		_, err := DialHost(t.Context(), "127.0.0.1", deviceOpts(830, jumpSrv.Addr())...)
		be.Nonzero(t, err)
		be.In(t, "jump host", err.Error())
		recvWithin(t, jump.gone, "the jump host to see its connection end")

		// every credential there is for a jump host was tried by now
		checkJump(t, seen)
		be.True(t, seen.saw(homePub))
	})

	t.Run("the identity file logs in to the jump host", func(t *testing.T) {
		home, homePub := sshHome(t)
		jumpSrv := newTestServer(t)
		trust(t, home, jumpSrv.Addr(), jumpSrv.hostKey)
		seen := recordAuth(jumpSrv, homePub)
		serveJump(jumpSrv)

		device := newTestServer(t)
		device.config.NoClientAuth = false
		device.config.PasswordCallback = func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if conn.User() != "admin" || string(password) != "device-secret" {
				return nil, errors.New("bad credentials")
			}
			return nil, nil
		}
		device.Serve(greet)
		_, port := hostPort(t, device.Addr())

		tr, err := DialHost(t.Context(), "127.0.0.1", deviceOpts(port, jumpSrv.Addr())...)
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
		checkJump(t, seen)
	})

	// Credentials given for the device switch the agent off for the device,
	// not for the jump host.
	t.Run("the agent logs in to the jump host", func(t *testing.T) {
		home := t.TempDir()
		be.NilErr(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
		t.Setenv("HOME", home)
		sock, agentPub := testAgent(t)
		t.Setenv("SSH_AUTH_SOCK", sock)

		jumpSrv := newTestServer(t)
		trust(t, home, jumpSrv.Addr(), jumpSrv.hostKey)
		seen := recordAuth(jumpSrv, agentPub)
		serveJump(jumpSrv)

		device := newTestServer(t)
		deviceSeen := recordAuth(device, devicePub)
		device.Serve(greet)
		_, port := hostPort(t, device.Addr())

		tr, err := DialHost(t.Context(), "127.0.0.1", deviceOpts(port, jumpSrv.Addr())...)
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
		checkJump(t, seen)
		be.False(t, deviceSeen.saw(agentPub))
	})
}

// WithInsecureIgnoreHostKey is for the device.  The host key of a jump host
// is verified whatever the caller says about the device.
func TestDialHost_ProxyJumpHostKey(t *testing.T) {
	tests := []struct {
		name  string
		known func(t *testing.T, home string, srv *testServer)
	}{
		{
			name:  "unknown",
			known: func(*testing.T, string, *testServer) {},
		},
		{
			name: "another key on file",
			known: func(t *testing.T, home string, srv *testServer) {
				_, other := writeKey(t, "")
				trust(t, home, srv.Addr(), other)
			},
		},
		{
			name: "known as the device only",
			known: func(t *testing.T, home string, srv *testServer) {
				trust(t, home, net.JoinHostPort("127.0.0.1", strconv.Itoa(DefaultPort)), srv.hostKey)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := sshHome(t)
			// a known hosts file that has somebody else in it
			_, other := writeKey(t, "")
			trust(t, home, "192.0.2.1:22", other)

			jumpSrv := newTestServer(t)
			tc.known(t, home, jumpSrv)
			jump := serveJump(jumpSrv)

			_, err := DialHost(t.Context(), "127.0.0.1",
				WithPassword(""),
				WithInsecureIgnoreHostKey(),
				WithProxyJump(jumpSrv.Addr()),
			)
			be.Nonzero(t, err)
			be.In(t, "jump host", err.Error())
			be.Equal(t, 0, len(jump.dests))
		})
	}
}

// Where the known hosts are is said per host: the ssh config's file for the
// jump host is where its key is looked for, and the file it names for the
// device is nothing to the jump host.
func TestDialHost_ProxyJumpKnownHostsOfConfig(t *testing.T) {
	// setup starts a device and a jump host and returns an ssh config in
	// which each has a known hosts file of its own.  The arguments say whose
	// key is in which: "jump host" or "device".
	setup := func(t *testing.T, jumpFileHas, deviceFileHas string) (config string, port int) {
		t.Helper()
		sshHome(t)

		jumpSrv := newTestServer(t)
		jumpHostname, jumpPort := hostPort(t, jumpSrv.Addr())
		serveJump(jumpSrv)

		device := newTestServer(t)
		device.Serve(greet)
		deviceHostname, port := hostPort(t, device.Addr())

		dir := t.TempDir()
		write := func(name, has string) string {
			srv := map[string]*testServer{"jump host": jumpSrv, "device": device}[has]
			path := filepath.Join(dir, name)
			line := knownhosts.Line([]string{knownhosts.Normalize(srv.Addr())}, srv.hostKey)
			be.NilErr(t, os.WriteFile(path, []byte(line+"\n"), 0o600))
			return path
		}

		config = filepath.Join(dir, "config")
		be.NilErr(t, os.WriteFile(config, fmt.Appendf(nil, `
Host bastion
    HostName %s
    Port %d
    UserKnownHostsFile %s

Host switch1
    HostName %s
    ProxyJump bastion
    UserKnownHostsFile %s
`, jumpHostname, jumpPort, write("known_jump", jumpFileHas), deviceHostname, write("known_device", deviceFileHas)), 0o600))
		return config, port
	}

	t.Run("each in its own file", func(t *testing.T) {
		config, port := setup(t, "jump host", "device")

		tr, err := DialHost(t.Context(), "switch1", WithPort(port), WithPassword(""), WithSSHConfig(config))
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
	})

	t.Run("the jump host only in the file of the device", func(t *testing.T) {
		config, port := setup(t, "device", "jump host")

		_, err := DialHost(t.Context(), "switch1", WithPort(port), WithPassword(""), WithSSHConfig(config),
			WithInsecureIgnoreHostKey())
		be.Nonzero(t, err)
		be.In(t, "jump host bastion: ", err.Error())
		be.In(t, "key is unknown", err.Error())
	})
}

// A dial that fails behind a jump host leaves no connection to it behind.
func TestDialHost_ProxyJumpFailureClosesJumpHost(t *testing.T) {
	t.Run("the device refuses the login", func(t *testing.T) {
		home, _ := sshHome(t)
		jumpAddr, jump := newJump(t, home)

		device := newTestServer(t)
		recordAuth(device, nil)
		device.Serve(greet)
		host, port := hostPort(t, device.Addr())

		_, err := DialHost(t.Context(), host,
			WithPort(port), WithPassword("wrong"), WithInsecureIgnoreHostKey(), WithProxyJump(jumpAddr))
		be.Nonzero(t, err)
		be.False(t, strings.Contains(err.Error(), "jump host"))
		recvWithin(t, jump.gone, "the jump host to see its connection end")
	})

	t.Run("nothing listens at the device", func(t *testing.T) {
		home, _ := sshHome(t)
		jumpAddr, jump := newJump(t, home)

		_, err := DialHost(t.Context(), "127.0.0.1",
			WithPort(1), WithPassword(""), WithInsecureIgnoreHostKey(), WithProxyJump(jumpAddr))
		be.Nonzero(t, err)
		be.In(t, "127.0.0.1:1 through jump host 127.0.0.1", err.Error())
		recvWithin(t, jump.gone, "the jump host to see its connection end")
	})

	// The device takes the connection and then says nothing: only the
	// context gets the dial out of the handshake.
	t.Run("the context expires during the handshake with the device", func(t *testing.T) {
		home, _ := sshHome(t)
		jumpAddr, jump := newJump(t, home)

		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "localhost:0")
		be.NilErr(t, err)
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}()
		host, port := hostPort(t, ln.Addr().String())

		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()

		// a dial the context does not end never returns
		dialed := make(chan error, 1)
		go func() {
			_, err := DialHost(ctx, host,
				WithPort(port), WithPassword(""), WithInsecureIgnoreHostKey(), WithProxyJump(jumpAddr))
			dialed <- err
		}()
		be.True(t, errors.Is(recvWithin(t, dialed, "the dial to give up"), context.DeadlineExceeded))
		recvWithin(t, jump.gone, "the jump host to see its connection end")
	})
}

// A jump host is an ssh server like any other and has a host key of every
// type.  The one on file has to be the one asked for: left to its own
// preference the client is shown another, which it does not know.
func TestDialHost_ProxyJumpHostKeyType(t *testing.T) {
	home, _ := sshHome(t)

	jumpSrv := newTestServer(t)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	be.NilErr(t, err)
	otherSigner, err := ssh.NewSignerFromKey(other)
	be.NilErr(t, err)
	jumpSrv.config.AddHostKey(otherSigner)
	trust(t, home, jumpSrv.Addr(), jumpSrv.hostKey)
	serveJump(jumpSrv)

	device := newTestServer(t)
	device.Serve(greet)
	host, port := hostPort(t, device.Addr())

	tr, err := DialHost(t.Context(), host,
		WithPort(port), WithPassword(""), WithInsecureIgnoreHostKey(), WithProxyJump(jumpSrv.Addr()))
	be.NilErr(t, err)
	readGreeting(t, tr)
	be.NilErr(t, tr.Close())
}

// The keepalive asks the device, not the jump host: a device that died
// behind a jump host that is fine has to end the transport.
func TestDialHost_ProxyJumpKeepalive(t *testing.T) {
	home, _ := sshHome(t)
	jumpAddr, jump := newJump(t, home)

	device := newTestServer(t)
	device.IgnoreRequests = true
	device.Serve(func(ch ssh.Channel) error {
		_, err := io.Copy(io.Discard, ch)
		return err
	})
	host, port := hostPort(t, device.Addr())

	tr, err := DialHost(t.Context(), host,
		WithPort(port),
		WithPassword(""),
		WithInsecureIgnoreHostKey(),
		WithProxyJump(jumpAddr),
		WithKeepalive(20*time.Millisecond, 2),
	)
	be.NilErr(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	recvWithin(t, jump.gone, "the keepalive to close the transport")
}
