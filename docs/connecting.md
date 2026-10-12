# Connecting

`ncssh.DialHost` follows the conventions of the OpenSSH client:

| Option | Effect |
| --- | --- |
| `WithPort(n)` | TCP port, default 830 |
| `WithUser(name)` | Username, default from the ssh config, then the OS user |
| `WithPrivateKeyFile(path, passphrase)` | Public key authentication from a key file |
| `WithSigner(signers...)` | Public key authentication with keys the program holds itself: one from a vault, a certificate signed for the session. For the device only, never offered to a jump host |
| `WithAgent()` | Keys from the ssh-agent; implied when no other authentication is given |
| `WithPassword(pw)` | Password authentication, also over keyboard-interactive when the device asks for the password only |
| `WithSSHConfig(path)` | Resolve `HostName`, `User`, `IdentityFile`, `UserKnownHostsFile` and `ProxyJump` from an ssh config; `""` means `~/.ssh/config` |
| `WithProxyJump(hosts...)` | Reach the device through jump hosts, each `[user@]host[:port]`, like `ssh -J` |
| `WithKnownHosts(files...)` | Host key files, default the `UserKnownHostsFile` of the ssh config, then `~/.ssh/known_hosts` |
| `WithHostKey(keys...)` | Hold the device to one of these host keys; no known_hosts file is read for it |
| `WithInsecureIgnoreHostKey()` | Skip host key verification for the device |
| `WithKeepalive(interval, maxMissed)` | Close the transport when the peer stops answering SSH keepalives |
| `WithCommand(command)` | Start NETCONF with a command in place of the netconf subsystem: `xml-mode netconf need-trailer` on port 22 of a Junos device that has no NETCONF service configured |
| `WithDialer(fn)` | Make the first connection yourself: one a device opened towards the program, one from a source address, one through a proxy |
| `WithClientConfig(fn)` | Adjust the generated `ssh.ClientConfig`, e.g. algorithms for old devices |

Without `WithPassword`, `WithPrivateKeyFile` or `WithSigner`, the ssh-agent
and the usual identity files are tried. An agent that does not answer, one
that waits for somebody to confirm the use of a key, holds the dial until
its context ends and no longer. A host that known_hosts only lists for port 22 is
accepted on the NETCONF port as well.

A program without a known_hosts file, in a container for one, gives the key
of the device, from its inventory, in place of switching the check off:

```go
hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHostKeyOfTheDeviceFromTheInventory"))
// ...
tr, err := ncssh.DialHost(ctx, "switch1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithHostKey(hostKey),
)
```

The key is a whole key and not a fingerprint: the device is asked for a key
of that type, so one with several host keys presents the one that can
match. Jump hosts are not covered by it and stay with the known hosts.
`WithHostKey` with no key, the list of an inventory that has none for the
device, fails the dial before it connects, also next to
`WithInsecureIgnoreHostKey`.

The same program has its private key from a vault and not from a file:

```go
signer, err := ssh.ParsePrivateKey(pemFromTheVault)
// ...
tr, err := ncssh.DialHost(ctx, "switch1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithSigner(signer),
	ncssh.WithHostKey(hostKey),
)
```

A certificate signed for the session goes in the same way, as the signer
`ssh.NewCertSigner` returns. `WithSigner` with no signer still names the
login: the identity files and the agent are not tried in its place, and a
dial with no other login fails before it connects.

The context bounds the whole attempt. When it ends the attempt, the error
matches `context.DeadlineExceeded` or `context.Canceled` and says what was
going on, and with which host:

```
ssh handshake with 192.0.2.7:830: context deadline exceeded
connecting to 192.0.2.7:830 through jump host bastion: context deadline exceeded
```

An ssh config is read only with `WithSSHConfig`: without it a host alias
from `~/.ssh/config` does not resolve and its `ProxyJump` is not followed.
`Port` is not taken from the ssh config: it names the login port, not the
NETCONF port.

## Jump hosts

A device that is only reachable through another host is dialed with
`WithProxyJump`, or with `WithSSHConfig` when the ssh config names a
`ProxyJump` for it:

```go
tr, err := ncssh.DialHost(ctx, "switch1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithPassword(devicePassword),
	ncssh.WithProxyJump("jump.example.net"),
)
```

The options describe the device, as the command line of `ssh` does. A jump
host is logged in to the way `ssh` logs in to it: as the current user on
port 22 with the ssh-agent and the default identity files, or with the
`User`, `Port`, `HostName`, `IdentityFile` and `UserKnownHostsFile` of its
block in the ssh config. It is never offered the device's password, key
file or signer.

Its host key has to be among the known hosts, also with
`WithInsecureIgnoreHostKey` and also where the ssh config says
`StrictHostKeyChecking no` or `accept-new`: a jump host that is new is
refused, where `ssh` would add it. Log in to it with `ssh` once from the
account that runs the program, or give a file with its key to
`WithKnownHosts`.

Closing the session closes the connections to the jump hosts. A keepalive
probes the device through them, so it notices a dead jump host as well.
`ProxyCommand` is not supported, and neither is a jump host whose own block
names a `ProxyJump`: list every jump host in order instead, or use the
`ssh` of the system, below.

## The ssh of the system

`ncssh` is an ssh client of its own and does not do everything OpenSSH
does: `ProxyCommand`, a jump host behind a jump host in the ssh config,
connection sharing, Kerberos, a key on a hardware token. `ssh -s` does all
of it and speaks NETCONF on its standard input and output. Package
`transport/command` runs a session over such a command:

```go
tr, err := nccmd.Start(exec.Command("ssh", "-s", "-p", "830", "router1.example.net", "netconf"))
if err != nil {
	return err
}
session, err := netconf.Open(ctx, tr)
```

What to know about it:

- The command gets pipes and no terminal, so `ssh` cannot ask for a
  password or a passphrase. The login has to need no typing: a key without
  a passphrase, the ssh-agent, a certificate, a connection that is shared
  already.
- The transport owns the process. `Close` kills it and waits for it, and so
  does a context that ends `Open`. Only the process itself is killed: give
  it the program, not a shell around it.
- A command that exits says why in the error: its exit status, and the end
  of what it wrote to standard error, such as `Permission denied
  (publickey)`. `errors.As` finds the `*exec.ExitError`, unless the
  command exited with status 0. A child that the command left behind
  delays that and does not hide it: the status is waited for as long as
  `cmd.WaitDelay` gives a child that holds the standard error, a second
  unless it is set, and with the standard output held the output is over
  once nothing has come for a second after the command was waited for. A
  child that keeps writing keeps it open, and so does Windows, whose pipes
  take no deadline.
- Nothing of `ncssh` applies. The host key check is that of `ssh` and its
  known_hosts, and a keepalive is `-o ServerAliveInterval=15`.

It passes the conformance suite of the transports, and the integration
suite runs over `ssh -s` against netopeer2 with a key and against Junos 26.2
on a vJunos-switch through an ssh config with a jump host. The lab device
takes a password only, which `sshpass` typed there in front of `ssh`.

## A device without a NETCONF service

A Junos device that has no `system services netconf ssh` has no port 830,
and refuses the netconf subsystem on port 22: the dial fails with
`ncssh.ErrSubsystemRefused`. Its ssh service starts NETCONF all the same
when it is told to with a command:

```go
tr, err := ncssh.DialHost(ctx, "router.example.com",
	ncssh.WithUser("admin"),
	ncssh.WithPort(22),
	ncssh.WithCommand("xml-mode netconf need-trailer"),
)
```

Seen on Junos 26.2 (vJunos-router) with the NETCONF service taken off: the
subsystem is refused on port 22 and the command gives a session. With the
service configured, on vJunos-router, vJunos-switch and vJunosEvolved,
port 22 takes both, and a session started with the command has the hello of
port 830, with the same capabilities and the same framing; the read tests
of the integration suite pass on it. Port 830 takes the subsystem only.
Nothing falls back from the subsystem to the command by itself.

One device setup needs an option more. The command asks Junos for the
`]]>]]>` trailer, and Junos keeps to that also where it is configured for
chunked framing (`rfc-compliant` and `version-1.1`) and says `base:1.1` in
its hello. A session that takes the hello at its word reads the first
reply as a chunk and ends with `transport.ErrMalformedChunk`. Offer
`base:1.0` only on such a session:

```go
session, err := netconf.Open(ctx, tr, netconf.WithCapability(netconf.CapNetConf10))
```

Seen on vJunos-router 26.2, where the read tests pass that way.

## A connection of your own

`DialHost` opens the TCP connection itself. `WithDialer` takes a function
that makes it, and the dial goes on from there: the host key check, the
login, the keepalive and the close are the same.

```go
// from an address of your choosing
dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("192.0.2.10")}}
tr, err := ncssh.DialHost(ctx, "switch1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithDialer(dialer.DialContext),
)

// on a connection a device opened towards the program
conn, err := listener.Accept()
// ...
tr, err = ncssh.DialHost(ctx, "switch1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithDialer(func(context.Context, string, string) (net.Conn, error) { return conn, nil }),
)
```

The host given to `DialHost` names the device, whatever the connection is:
its known_hosts entry and its login are looked up under that name, so a
connection that came in from anywhere is held to the host key of the device
it claims to be. The transport owns the connection from when the function
returns it, and closes it, also one that came together with an error. A
function that returns neither fails the dial. A dial that fails before it connects, over a key
file or known hosts that are not there, never calls the function, and a
connection the program made beforehand is still the program's to close.
With jump hosts the function makes the connection to the first of them.
Accepting connections and knowing which device one is from stay with the
program.

## When connecting fails

A program that connects to many devices has to tell the failures apart: one
is fixed with another password, one on the device, one by waiting.

```go
tr, err := ncssh.DialHost(ctx, host, ncssh.WithUser("automation"))

var (
	keyErr *knownhosts.KeyError
	algErr *ssh.AlgorithmNegotiationError
)
switch {
case err == nil:
case errors.Is(err, ncssh.ErrAuth):
	// the device, or a jump host, did not accept the login
case errors.Is(err, ncssh.ErrSubsystemRefused):
	// logged in, and no NETCONF there: not enabled, or on another port
case errors.As(err, &keyErr) && len(keyErr.Want) == 0:
	// the host is not among the known hosts
case errors.As(err, &keyErr):
	// the host has another key than the known one, or than the one given
	// with WithHostKey: keyErr.Want
case errors.As(err, &algErr):
	// no algorithm in common, an old device: see WithClientConfig
case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
	// the context ended the dial, and the error says in which step
default:
	// the network: refused, unreachable, reset.  Also what kept the dial
	// from starting, a key file, an ssh config or known hosts that are not
	// there, and a WithCommand that the device did not run
}
```

`ErrAuth` says who tried and what was offered: `ssh: login failed: user
"automation" at 192.0.2.7:830, offered 2 keys and the password: ...`.
The key and algorithm errors are those of `golang.org/x/crypto/ssh` and its
`knownhosts` package. A wrong password and a user that does not exist are
the same to Junos, and so to `ErrAuth`.

The hello exchange can fail after all of that went well:

```go
session, err := netconf.Open(ctx, tr)

var helloErr *netconf.HelloError
if errors.As(err, &helloErr) {
	log.Printf("no session: %v", helloErr.Err)
	for i := range helloErr.RPCErrors {
		log.Printf("the device said: %s", helloErr.RPCErrors[i].Message)
	}
}
```

Such an error matches `netconf.ErrHello` as well.

A `HelloError` has what the device sent where its hello belongs: the
capabilities of a hello that made no session, RPC errors, and text outside
any element. A connection that ends before a hello is one too, and says
what that usually means: a wrong port, or a device without NETCONF enabled.
A device that prints a banner and a prompt and then waits is one when the
context of `Open` ends, with what it printed in `Text`.

Junos has a case of its own here. To a user whose login class permits
nothing (`unauthorized`), Junos 26.2 prints "error: Restricted user
session." as plain text, sends the same as an `<rpc-error>` in a reply that
answers nothing, and then says hello all the same; every request on such a
session is refused. `Open` returns no session for it but a `HelloError`
with both errors and the text. Warnings alone in front of a hello are
logged, and the session opens.

## TLS and ready-made SSH clients

`nctls.Dial` takes a `tls.Config` and verifies the server certificate
against the host it dialed:

```go
tr, err := nctls.Dial(ctx, "tcp", "router.example.com:6513", &tls.Config{
	RootCAs:      caPool,
	Certificates: []tls.Certificate{clientCert},
})
```

`ncssh.Dial` takes a ready-made `ssh.ClientConfig` and `ncssh.NewTransport`
an existing `*ssh.Client`, for programs that manage their SSH connections
themselves.

## Writing a transport

A transport is anything that implements `transport.Transport`: a reader and
a writer per message, the switch to chunked framing, and `Close`, which is
the one way to interrupt a read or a write. `transport.Framer` implements
the RFC 6242 framing on a byte stream. `transporttest.Run` is the
conformance suite that states what a session expects of a transport; the
ssh, tls and command transports pass it.
