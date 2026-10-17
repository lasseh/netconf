# Connecting

This page covers getting from a host name to an open NETCONF transport,
over SSH, over TLS or through the system `ssh`. Read it when the defaults
of `ncssh.DialHost` do not reach your device, or when a connection fails.
For a first run, see [Getting started](getting-started.md), and for a
symptom, [Troubleshooting](troubleshooting.md).

- [Options of `ncssh.DialHost`](#options-of-ncsshdialhost)
- [Host keys](#host-keys)
- [Authentication](#authentication)
- [The SSH config](#the-ssh-config)
- [Jump hosts](#jump-hosts)
- [System OpenSSH](#system-openssh)
- [A device without a NETCONF service](#a-device-without-a-netconf-service)
- [Custom connections with `WithDialer`](#custom-connections-with-withdialer)
- [When connecting fails](#when-connecting-fails)
- [TLS and ready-made SSH clients](#tls-and-ready-made-ssh-clients)
- [Writing a transport](#writing-a-transport)

## Options of `ncssh.DialHost`

`ncssh.DialHost` connects the way the OpenSSH client does. These options
change its defaults:

| Option | Effect |
| --- | --- |
| `WithPort(n)` | TCP port of the device. Default 830, `ncssh.DefaultPort`. |
| `WithUser(name)` | User name on the device. Default the `User` of the SSH config, else the OS user. |
| `WithPassword(pw)` | Password login, also over keyboard-interactive. |
| `WithPrivateKeyFile(path, passphrase)` | Public key login with a key file. `passphrase` may be empty. Repeatable. |
| `WithSigner(signers...)` | Public key login with keys or certificates the program holds. |
| `WithAgent()` | Adds the ssh-agent keys alongside a password, key file or signer. Fails the dial without an agent at `SSH_AUTH_SOCK`. |
| `WithSSHConfig(path)` | Read an SSH config file. `""` means `~/.ssh/config`. |
| `WithProxyJump(hosts...)` | Jump hosts, each `[user@]host[:port]` as for `ssh -J`. |
| `WithKnownHosts(files...)` | Host key files for the device and every jump host. |
| `WithHostKey(keys...)` | Hold the device to one of these host keys. |
| `WithInsecureIgnoreHostKey()` | Skip the host key check of the device. |
| `WithKeepalive(interval, maxMissed)` | Send an SSH keepalive every `interval`. After `maxMissed` misses in a row, the session ends with `ncssh.ErrKeepalive`. Off by default. |
| `WithCommand(command)` | Start NETCONF with a command, not the `netconf` subsystem. |
| `WithDialer(fn)` | Make the first connection yourself. |
| `WithClientConfig(fn)` | Change the built `ssh.ClientConfig`, such as the algorithms of an old device. `fn` runs last, so its `HostKeyCallback`, `HostKeyAlgorithms` or `Auth` wins. |

Paths given to `WithSSHConfig`, `WithPrivateKeyFile` and `WithKnownHosts`
are used as written. A leading `~` is not expanded, so
`WithPrivateKeyFile("~/.ssh/id_ed25519", "")` fails to find the file.

The context passed to `DialHost` bounds the whole attempt, from the TCP
connection to the start of NETCONF. When it ends the attempt, the error
matches `context.DeadlineExceeded` or `context.Canceled` and names the step
and the host, such as `ssh handshake with 192.0.2.7:830`. Once `DialHost`
has returned, its context has no effect on the transport.

## Host keys

`ncssh.DialHost` checks the host key of the device against `known_hosts`
files and never adds a key to them. The files are those given to
`ncssh.WithKnownHosts`, for the device and every jump host. Without it,
they are the `UserKnownHostsFile` files of the SSH config, or else
`~/.ssh/known_hosts`. Missing `UserKnownHostsFile` files are skipped, and
the dial fails when none exists. `/etc/ssh/ssh_known_hosts` and
`~/.ssh/known_hosts2`, which OpenSSH reads by default, are read only when
listed this way.

`DialHost` looks up a host under the name it dials, the SSH config's
`HostName` or else the host given, never under the IP address. An entry
for port 22 counts for the NETCONF port only when the files have nothing
for the NETCONF port, never after a key mismatch or a revoked key.
`DialHost` asks the device for the key types on file or given to
`WithHostKey`, so a device with several host keys presents one that can
match.

A program without a `known_hosts` file can pass the device's key from its
inventory with `ncssh.WithHostKey` instead of turning the check off:

```go
// hostKeyFromTheInventory is the device's public key in authorized_keys
// format, such as "ssh-ed25519 AAAA...".
hostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostKeyFromTheInventory)
if err != nil {
	return err
}
tr, err := ncssh.DialHost(ctx, "switch1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithHostKey(hostKey),
)
```

`WithHostKey` takes whole public keys, not fingerprints, and each call adds
to the list. An RSA key also matches with rsa-sha2 signatures.
`WithHostKey` covers the device only and wins over
`ncssh.WithInsecureIgnoreHostKey`. Given no key, it fails the dial before
it connects.

## Authentication

Without `ncssh.WithPassword`, `ncssh.WithPrivateKeyFile` or
`ncssh.WithSigner`, `ncssh.DialHost` tries these:

- the `IdentityFile` entries of the SSH config, else `~/.ssh/id_ed25519`,
  `~/.ssh/id_ecdsa` and `~/.ssh/id_rsa`. A file that is missing,
  unreadable or encrypted is skipped.
- the ssh-agent, when `SSH_AUTH_SOCK` is set and the agent can be reached

Given a password, key file or signer, `DialHost` offers only those, and the
agent only with `ncssh.WithAgent`. A key file that cannot be read or parsed
fails the dial. `WithSigner` with no signer still counts as a chosen
login, so a dial with no other login fails before it connects.

All keys go to the device in one public key attempt, in this order:
signers, key files, then the agent's keys. The password follows, as
password authentication and then over keyboard-interactive, where it
answers one hidden prompt, once. A device that asks for more, such as a
second factor, fails the login with `ncssh.ErrAuth`.

An agent that waits, such as for a touch, holds the dial until its context
ends. `ncssh` offers every key the agent lists, hardware token keys such
as `sk-ssh-ed25519@openssh.com` included, but cannot use a token without
an agent. A `_sk` key file does not parse, and `PKCS11Provider` is not
supported. For a token that no agent holds, use
[System OpenSSH](#system-openssh).

A program that keeps its private key in a vault passes it with
`ncssh.WithSigner`, here with the host key from [Host keys](#host-keys):

```go
signer, err := ssh.ParsePrivateKey(pemFromTheVault)
if err != nil {
	return err
}
tr, err := ncssh.DialHost(ctx, "switch1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithSigner(signer),
	ncssh.WithHostKey(hostKey),
)
```

Pass a certificate signed for the session the same way, as the signer
from `ssh.NewCertSigner`. For a device that wants the old `ssh-rsa`
signature, wrap the RSA signer with `ssh.NewSignerWithAlgorithms`.

## The SSH config

`ncssh.DialHost` reads an SSH config file only with `ncssh.WithSSHConfig`.
Without it, a host alias from `~/.ssh/config` does not resolve and its
`ProxyJump` is not followed. With it, `DialHost` reads only these settings
from each host's block:

- `HostName`. `%h` in it becomes the host given. No other token is expanded
  anywhere in the file.
- `User`, for the device unless `ncssh.WithUser` is given.
- `IdentityFile`, for the device unless a password, key file or signer is
  given. A leading `~` is expanded. A file that is missing or does not
  parse is skipped, as is a path with a token such as `%h`.
- `UserKnownHostsFile`, unless `ncssh.WithKnownHosts` is given.
- `ProxyJump`, for the device unless `ncssh.WithProxyJump` is given.
- `Port`, for jump hosts only. The SSH config names the login port, so the
  port of the device comes from `ncssh.WithPort`.
- `ProxyCommand`, only to refuse it. It fails the dial in the device's
  block unless `WithProxyJump` is given, and in the first jump host's
  block, like a `ProxyJump` there.

`Include` is followed. `Match all` and `Match host` blocks apply like `Host`
blocks, with any criteria after `Match host` read as more host patterns.
Any other `Match`, such as `Match user`, fails every dial with that file,
also from an included file, with an error that starts with
`failed to parse ssh config`.

`/etc/ssh/ssh_config` is not read, and every other directive is ignored,
including these:

- `StrictHostKeyChecking`. An unknown host key is refused whatever the
  file says, for the device and every jump host.
- `HostKeyAlias` and `GlobalKnownHostsFile`. See [Host keys](#host-keys).
- `IdentitiesOnly`. The agent's keys are offered alongside the identity
  files whenever `SSH_AUTH_SOCK` is set.
- `ProxyJump` and `ProxyCommand` in the block of a later jump host, which
  is reached through the one before it.

## Jump hosts

To use jump hosts, pass `ncssh.WithProxyJump` to `ncssh.DialHost`, or
`ncssh.WithSSHConfig` when the SSH config names a `ProxyJump` for the
device:

```go
tr, err := ncssh.DialHost(ctx, "switch1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithPassword(devicePassword),
	ncssh.WithProxyJump("bastion.example.net"),
)
```

The dial options describe the device. Only
`WithKnownHosts`, `WithAgent` and the file of `WithSSHConfig` also apply to
every jump host, and `WithDialer` makes the connection to the first one.

`DialHost` logs in to a jump host the way `ssh` does. The user and port
come from the entry, such as `automation@bastion.example.net:22`, else from
the `User` and `Port` of its block, else they are the OS user and 22. Its
keys are the `IdentityFile` entries of its block, else the default identity
files, with the ssh-agent when `SSH_AUTH_SOCK` is set.

The host key of a jump host is always checked against the known hosts,
even with `WithInsecureIgnoreHostKey` or `WithHostKey`, so a new jump host
is refused where `ssh` would add its key. Log in to it once with `ssh` from
the account that runs the program, or pass a file with its key to
`WithKnownHosts`. That replaces the default for the device too, so list
the device's file as well.

Closing the session closes the jump host connections. A keepalive probes
the device through them, so it notices a dead jump host too.

List every jump host in order instead of chaining them in the SSH config.
`WithProxyJump("none")`, or `WithProxyJump()` with no hosts, switches off a
`ProxyJump` from the SSH config. For a `ProxyCommand` or a chain in the SSH
config, use [System OpenSSH](#system-openssh).

## System OpenSSH

`ncssh` implements SSH in Go. It has no
`ProxyCommand`, no jump host chained in the SSH config, no connection
sharing and no Kerberos, and it cannot use a hardware token that no agent
holds. The system `ssh` has all of that, and `nccmd.Start` from package
`transport/command` runs a session over the standard input and output of
`ssh -s`:

```go
tr, err := nccmd.Start(exec.Command("ssh", "-s", "-p", "830", "router1.example.net", "netconf"))
if err != nil {
	return err
}
session, err := netconf.Open(ctx, tr)
```

Rules for `nccmd.Start`:

- **No terminal.** `ssh` cannot ask for a password or a passphrase, so use
  a login that needs no typing, such as the ssh-agent.
- **`exec.Command`, not `exec.CommandContext`.** A command context kills
  `ssh`, and the session with it, when it ends. Bound connecting with the
  context of `netconf.Open` instead.
- **The transport owns the process.** `Close`, or a context that ends
  `netconf.Open`, kills the process and waits for it. Only that process is
  killed, so start the program, not a shell around it.
- **Leave `Stdin`, `Stdout` and `Stderr` unset.** `nccmd.Start` refuses a
  set `Stdin` or `Stdout`. With `Stderr` set, the error of a command that
  exits lacks its standard error.
- **A command that exits says why.** Reads and writes fail with the exit
  status and the end of the standard error, such as
  `Permission denied (publickey)`. `errors.As` finds the `*exec.ExitError`,
  unless the status was 0.
- **A child process left running delays that error.** A child that holds
  the standard error delays it by up to `cmd.WaitDelay`, one second unless
  set. A child that holds the standard output delays it until nothing has
  arrived for one second after `cmd.Wait` returned. A child that keeps
  writing keeps the output open, and on Windows so does any child that
  holds it.
- **No `ncssh` option applies.** `ssh` checks the host key against its own
  `known_hosts`, and `-o ServerAliveInterval=15` turns on a keepalive.

## A device without a NETCONF service

Junos 26.2 on vJunos-router, with no `system services netconf ssh`
configured, has no port 830 and refuses the `netconf` subsystem on port 22.
`ncssh.DialHost` then fails with `ncssh.ErrSubsystemRefused`. The SSH
service still starts NETCONF for a command, which `ncssh.WithCommand` sends
in place of the subsystem request:

```go
tr, err := ncssh.DialHost(ctx, "router1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithPort(22),
	ncssh.WithCommand("xml-mode netconf need-trailer"),
)
```

`DialHost` never falls back from the subsystem to the command. The dial
returns at the first byte of the device's hello, so against a device that
waits for the client's hello it runs until its context ends. Junos 26.2
sends its hello unasked on this path. A command that ends without output
fails the dial with what it wrote to standard error.

With the NETCONF service configured, Junos 26.2 on vJunos-router takes
both the subsystem and the command on port 22, and port 830 takes the
subsystem only. See
[Sessions started with a command](junos-verified.md#sessions-started-with-a-command).

With chunked framing configured, through `rfc-compliant` and
`version-1.1`, Junos 26.2 on vJunos-router still sends the `]]>]]>` trailer
that the command asks for, while its hello advertises `base:1.1`. A session
that follows the hello ends with `transport.ErrMalformedChunk`. Offer
`base:1.0` only:

```go
session, err := netconf.Open(ctx, tr, netconf.WithCapability(netconf.CapNetConf10))
```

## Custom connections with `WithDialer`

`ncssh.WithDialer` takes a function that makes the TCP connection for
`ncssh.DialHost`, such as one through a proxy.

```go
// from a source address of your choosing
dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("192.0.2.10")}}
tr, err := ncssh.DialHost(ctx, "switch1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithDialer(dialer.DialContext),
)

// on a connection the device opened toward the program
conn, err := listener.Accept()
if err != nil {
	return err
}
tr, err = ncssh.DialHost(ctx, "switch1.example.net",
	ncssh.WithUser("automation"),
	ncssh.WithDialer(func(context.Context, string, string) (net.Conn, error) { return conn, nil }),
)
```

The function is called once, with the dial's context, `"tcp"` and the
address `DialHost` would have dialed. With jump hosts, it connects to the
first one. The host passed to `DialHost` still names the device, so any
connection is checked against that device's host key.

The transport owns the connection once the function returns it, even with
an error, and closes it when the dial fails or the transport closes.
Returning neither a connection nor an error fails the dial. A dial that
fails earlier, such as on an unreadable key file, never calls the
function, so a connection opened beforehand stays the program's to close.

The program must accept connections, tell which device each comes from,
and read any exchange that precedes SSH before the function returns the
connection. Junos `outbound-ssh` is untested, see
[Not verified on a device](junos-verified.md#not-verified-on-a-device).

## When connecting fails

These checks tell the failures of `ncssh.DialHost` apart:

```go
tr, err := ncssh.DialHost(ctx, host, ncssh.WithUser("automation"))

var (
	keyErr     *knownhosts.KeyError
	revokedErr *knownhosts.RevokedError
	algErr     *ssh.AlgorithmNegotiationError
)
switch {
case err == nil:
case errors.Is(err, ncssh.ErrAuth):
	// the device or a jump host refused the login
case errors.Is(err, ncssh.ErrSubsystemRefused):
	// logged in, but no NETCONF there: not enabled, or on another port
case errors.As(err, &revokedErr):
	// the host key is marked @revoked in known_hosts
case errors.As(err, &keyErr) && len(keyErr.Want) == 0:
	// the host is not among the known hosts
case errors.As(err, &keyErr):
	// a key other than the known one or the WithHostKey ones: see keyErr.Want
case errors.As(err, &algErr):
	// no algorithm in common: an old device, see WithClientConfig, or a
	// WithHostKey key of a type the device does not have
case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
	// the context ended the dial, and the error names the step
default:
	// the network, a missing key file, SSH config or known_hosts file,
	// or a WithCommand that the device did not run
}
```

`ncssh.ErrAuth` says who tried and what was offered, such as `ssh: login
failed: user "automation" at 192.0.2.7:830, offered 2 keys and the
password: ...`, and never contains the password. A wrong password, an
unknown key and an unknown user all end in `ErrAuth`, and Junos does not
say which it was. The key and algorithm errors come from
`golang.org/x/crypto/ssh` and its `knownhosts` package.

The hello exchange in `netconf.Open` can fail after the dial succeeded,
with an error that matches `netconf.ErrHello`:

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

A `*netconf.HelloError` holds what the device sent, up to and including
its hello. Its `RPCErrors` are the RPC errors sent before or in place of
the hello, warnings included. `Text` is what the device printed outside
any element. `Capabilities` are those of a hello that opened no session,
and `Err` says what went wrong.

A connection that ends before a hello also fails with a `HelloError`, which
names the usual causes, such as a wrong port. A device that prints a banner
and waits fails `Open` when its context ends, with the banner in `Text`.

`netconf.Open` opens no session when an `<rpc-error>` other than a warning
comes before the hello. Warnings alone are logged, and the session opens.
Junos 26.2 on vJunos-router sends a warning and
`error: Restricted user session.` this way to a user whose login class is
`unauthorized`, see [Captured replies](junos-verified.md#captured-replies).
Junos 26.2 would refuse every operation from that user anyway.

## TLS and ready-made SSH clients

`nctls.Dial` opens NETCONF over TLS, RFC 7589. `nctls` has no default
port, so the address names one, such as 6513 from the RFC:

```go
tr, err := nctls.Dial(ctx, "tcp", "router1.example.net:6513", &tls.Config{
	RootCAs:      caPool,
	Certificates: []tls.Certificate{clientCert},
})
```

`nctls.Dial` checks the server certificate against `ServerName` when set,
else against the host in the address. A nil config means the system roots
and TLS 1.2 or newer. The context bounds the TCP connection and the TLS
handshake. `nctls.NewTransport` takes a `*tls.Conn` that the program
opened. Closing either transport closes its TLS connection. No TLS run
against a Junos device is recorded.

`ncssh.Dial` takes a ready-made `*ssh.ClientConfig`, and its transport
closes the connection when it closes. `ncssh.NewTransport` takes an
existing `*ssh.Client` and leaves it to the caller. Closing that transport
releases everything waiting on it and closes its SSH channel. On a device
that has stopped answering, the channel goes only with the connection, so
a goroutine stays until the caller closes the client.

## Writing a transport

A transport implements `transport.Transport`: `MsgReader` and `MsgWriter`
for one message each, `Upgrade` for the switch to chunked framing, and
`Close`, the only way to interrupt a read or a write. `transport.Framer`
implements RFC 6242 framing on a byte stream, for embedding in a transport
that supplies `Close`.

Test a transport with the conformance suite,
`transporttest.Run(t, newPair)`, where `newPair` returns the transport
under test and a `transporttest.Peer` that plays the device. The SSH, TLS
and command transports pass it. The contract is in the
`transport.Transport` documentation,
[Ownership and session end](architecture.md#ownership-and-session-end) and
[ADR 0001](adr/0001-close-is-cancel-transport-contract.md).
