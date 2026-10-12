package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// DefaultPort is the IANA assigned port for NETCONF over SSH (RFC6242).
const DefaultPort = 830

type keyFile struct {
	path       string
	passphrase string
}

// environment is what DialHost takes from the process it runs in.
type environment struct {
	home      string // the user's home directory, empty when there is none
	noHome    error  // why there is none
	agentSock string // the ssh-agent socket, empty when there is none

	// user returns the OS user name.  It is asked only when no user is
	// given: finding out can take a lookup in a directory.
	user func() (string, error)
}

func processEnvironment() environment {
	env := environment{agentSock: os.Getenv("SSH_AUTH_SOCK"), user: currentUser}
	env.home, env.noHome = os.UserHomeDir()
	return env
}

// currentUser returns the name of the user the process runs as.
func currentUser() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// expandHome resolves a leading ~ in path.
func (env environment) expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	if env.home == "" {
		if env.noHome != nil {
			return "", fmt.Errorf("failed to expand %s: %w", path, env.noHome)
		}
		return "", fmt.Errorf("failed to expand %s: no home directory", path)
	}
	return filepath.Join(env.home, path[1:]), nil
}

type dialConfig struct {
	env environment

	port     int
	user     string
	password *string
	// keyFiles are the keys the caller asked for, identityFiles the ones
	// the ssh config or the defaults offer
	keyFiles      []keyFile
	identityFiles []keyFile
	// signers are keys the caller holds itself, and namedSigners says that
	// WithSigner was given, with signers or without
	signers      []ssh.Signer
	namedSigners bool
	agent        bool
	tweak        func(*ssh.ClientConfig)
	sshConfig    *string
	proxyJump    *string
	// knownHosts are the files the caller asked for, sshKnownHosts the ones
	// the ssh config names for the host
	knownHosts    []string
	sshKnownHosts []string
	insecure      bool
	// hostKeys are the keys the device has to present one of, and pinned
	// says that WithHostKey was given, with keys or without
	hostKeys []ssh.PublicKey
	pinned   bool
	// connect makes the first connection, nil for one that is dialed here
	connect dialFunc
	// command starts NETCONF in place of the netconf subsystem, when set
	command string

	keepaliveInterval  time.Duration
	keepaliveMaxMissed int

	// offered notes what the login methods built here were asked for
	offered loginOffer
}

// DialOption configures DialHost.
type DialOption interface {
	apply(*dialConfig)
}

type dialOptionFunc func(*dialConfig)

func (f dialOptionFunc) apply(cfg *dialConfig) { f(cfg) }

// WithPort sets the TCP port to connect to.  The default is DefaultPort
// (830); devices that only expose NETCONF on the regular SSH port need 22.
func WithPort(port int) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) { cfg.port = port })
}

// WithUser sets the username.  The default is the User from the ssh config
// file (see WithSSHConfig) and then the current OS user.
func WithUser(name string) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) { cfg.user = name })
}

// WithPassword enables password authentication, including
// keyboard-interactive authentication that asks for the password.
func WithPassword(password string) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) { cfg.password = &password })
}

// WithPrivateKeyFile enables public key authentication with the private key
// stored at path.  passphrase is only used for encrypted keys and may be
// empty otherwise.  The option can be given more than once.
func WithPrivateKeyFile(path, passphrase string) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) {
		cfg.keyFiles = append(cfg.keyFiles, keyFile{path: path, passphrase: passphrase})
	})
}

// WithSigner enables public key authentication with keys the caller holds
// itself: a key that came from a vault and never was a file, parsed with
// ssh.ParsePrivateKey, or a certificate signed for the session, made with
// ssh.NewCertSigner.  The option can be given more than once.
//
// A signer is a login that was named, like a key file: the identity files
// of the ssh config and the defaults are not tried next to it, and the
// agent only with WithAgent.  All keys of a dial are offered in one go, the
// signers first.  A device that wants the old ssh-rsa signature from an RSA
// key gets it from a signer wrapped with ssh.NewSignerWithAlgorithms.
//
// The option names the login also when it is given no signer, as with the
// list of a vault that has nothing for the device: nothing is tried in its
// place, and a dial that has no other login fails before it connects.
//
// The signers are for the device and are never offered to a jump host.
func WithSigner(signers ...ssh.Signer) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) {
		cfg.namedSigners = true
		cfg.signers = append(cfg.signers, signers...)
	})
}

// WithAgent enables public key authentication with the keys held by the
// ssh-agent listening on SSH_AUTH_SOCK, and fails the dial when there is no
// agent.  Without WithPassword, WithPrivateKeyFile or WithSigner the agent is
// used anyway when SSH_AUTH_SOCK is set, together with the identity files
// from the ssh config or, failing those, ~/.ssh/id_ed25519, id_ecdsa and
// id_rsa.
func WithAgent() DialOption {
	return dialOptionFunc(func(cfg *dialConfig) { cfg.agent = true })
}

// WithSSHConfig resolves the host through an OpenSSH client configuration
// file.  An empty path selects ~/.ssh/config.  The HostName, User,
// IdentityFile, UserKnownHostsFile and ProxyJump settings that apply to the
// host are honoured.  Port is not: the ssh config describes the login
// service, not the NETCONF subsystem port, so use WithPort.  Hosts that need
// ProxyCommand are rejected rather than dialed directly.
//
// Nothing else in the file has a say.  In particular a host key that is not
// among the known hosts is refused whatever StrictHostKeyChecking says, and
// HostKeyAlias and GlobalKnownHostsFile are not looked at.
//
// Jump hosts are resolved through the file as well, see WithProxyJump.
func WithSSHConfig(path string) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) { cfg.sshConfig = &path })
}

// WithProxyJump reaches the device through jump hosts, logged in to in the
// order given, each through the one before it.  A jump host is written like
// for the -J option of ssh: [user@]host[:port].  The option takes the place
// of a ProxyJump in the ssh config, and "none" or no jump host at all
// switches that one off.
//
// What the other options say is about the device, as ssh has it for its
// command line: WithUser, WithPort, WithPassword, WithPrivateKeyFile, WithSigner,
// WithInsecureIgnoreHostKey and WithClientConfig do not apply to a jump
// host.  A jump host is logged in to as the current user on port 22 with
// the keys of the ssh-agent and ~/.ssh/id_ed25519, id_ecdsa and id_rsa,
// unless the jump host as written or its block in the ssh config (see
// WithSSHConfig: HostName, User, Port, IdentityFile, UserKnownHostsFile)
// says otherwise.  It never gets to see the password.  Its host key always
// has to be among the known hosts, see WithKnownHosts: a jump host that is
// new is refused, also where the ssh config has ssh accept it
// (StrictHostKeyChecking no or accept-new).
//
// A jump host whose block in the ssh config names a ProxyJump of its own is
// refused: name every jump host on the way, in order.
func WithProxyJump(hosts ...string) DialOption {
	list := strings.Join(hosts, ",")
	return dialOptionFunc(func(cfg *dialConfig) { cfg.proxyJump = &list })
}

// WithKnownHosts verifies the server host key against the given OpenSSH
// known_hosts files.  The default is what the ssh config names for the host
// as UserKnownHostsFile (see WithSSHConfig), and without that
// ~/.ssh/known_hosts.  The system-wide /etc/ssh/ssh_known_hosts is not read
// unless it is given here.  A host only known by its entry for the regular
// SSH port is accepted on other ports too, as the OpenSSH client does.
func WithKnownHosts(files ...string) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) { cfg.knownHosts = files })
}

// WithInsecureIgnoreHostKey disables host key verification, leaving the
// connection open to man-in-the-middle attacks.  Only use it on networks you
// trust.
func WithInsecureIgnoreHostKey() DialOption {
	return dialOptionFunc(func(cfg *dialConfig) { cfg.insecure = true })
}

// WithHostKey holds the device to one of the given host keys: it has to
// present one of them, and no known_hosts file is read for it.  It is for a
// program that has no such file and knows the key of each device from
// somewhere else, an inventory, and would otherwise have to switch the check
// off.  The option can be given more than once, the keys add up.
//
// The keys are whole keys, as ssh.ParseAuthorizedKey and
// ssh.ParsePublicKey return them, and not fingerprints: the device is asked
// for a key of the types given, so one that has several host keys presents
// the one that can match.  An RSA key is accepted with the rsa-sha2
// signatures as well.  A device that has no key of such a type fails the
// handshake over the algorithms, and one with another key of that type with
// a *knownhosts.KeyError that has the keys given, as for a host whose key is
// not the known one.
//
// The keys are those of the device.  A jump host is checked against the
// known hosts, see WithProxyJump.  With the option given the check is made
// whatever WithInsecureIgnoreHostKey says.  That holds for an option that
// was given no key as well, the list of an inventory that has none for the
// device: no key can match, and the dial fails before it connects.
func WithHostKey(keys ...ssh.PublicKey) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) {
		cfg.pinned = true
		cfg.hostKeys = append(cfg.hostKeys, keys...)
	})
}

// WithDialer has fn make the first connection of the dial, which DialHost
// would open itself: the one to the device, or to the first jump host when
// there are any.  It is for a connection the program has already or wants
// to make its own way: one a device opened towards the program, as a Junos
// device with outbound-ssh does, one from a given source address, one
// through a proxy.  fn is called once, with the context of the dial, "tcp"
// and the address DialHost would have connected to, and may ignore all
// three.  A dial that fails before it connects, over a key file or known
// hosts it cannot read, does not call fn.
//
// Everything after the connection is as ever.  The host given to DialHost
// names the device: its known_hosts entry and its login are found under
// that name, so where the connection comes from or goes to proves nothing
// and does not have to.  The transport owns the connection from the moment
// fn returns it, and closes it when the dial fails and when the transport
// is closed.  A connection that fn was never asked for stays the caller's
// to close.
//
// An error of fn fails the dial, as "connecting to <host>: <error>", and a
// connection that came with the error is closed.  So does an fn that returns
// neither a connection nor an error.
func WithDialer(fn func(ctx context.Context, network, addr string) (net.Conn, error)) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) { cfg.connect = fn })
}

// WithCommand starts NETCONF on the device with a command, sent as an exec
// request, in place of the request for the netconf subsystem.  It is for a
// device that has no NETCONF service to ask for and starts NETCONF from its
// ssh service all the same.  On Junos that command is
//
//	xml-mode netconf need-trailer
//
// on port 22, which WithPort has to say: a Junos device without "system
// services netconf ssh" refuses the subsystem there and takes the command.
// Its NETCONF port, where there is one, takes the subsystem only.
//
// Nothing falls back from the one to the other.  A refused subsystem is
// ErrSubsystemRefused, for the caller to act on.
//
// The dial waits for the first byte the command sends, which is the
// beginning of a hello when NETCONF has started.  A command that ends
// without one fails the dial with what it wrote to standard error.  So the
// option is for a device that says hello without being asked, as Junos
// does on this path.  One that waits for the hello of the client says
// nothing, and the dial ends with its context.
func WithCommand(command string) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) { cfg.command = command })
}

// WithClientConfig lets fn adjust the ssh.ClientConfig DialHost built, right
// before connecting.  Use it for settings DialHost has no option for, such
// as the key exchange, cipher and host key algorithms an old device needs.
func WithClientConfig(fn func(*ssh.ClientConfig)) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) { cfg.tweak = fn })
}

// WithKeepalive sends an SSH keepalive request every interval and closes the
// transport after maxMissed consecutive requests went unanswered, so a
// silently dead peer surfaces as a closed session instead of RPCs that hang
// until their context expires.  maxMissed below 1 is treated as 1.
//
// A transport closed this way fails what is read from it and written to it
// with ErrKeepalive, and that is what the session ends with.
func WithKeepalive(interval time.Duration, maxMissed int) DialOption {
	return dialOptionFunc(func(cfg *dialConfig) {
		cfg.keepaliveInterval = interval
		cfg.keepaliveMaxMissed = max(maxMissed, 1)
	})
}

// DialHost connects to the NETCONF SSH subsystem on host using the same
// conventions as the OpenSSH client: known_hosts verification, ssh-agent and
// private key files and (with WithSSHConfig) host aliases and jump hosts.
// It is a convenience on top of Dial for callers that do not need to build
// their own ssh.ClientConfig.
//
// The context bounds the whole connection attempt.  An attempt that it ends
// fails with the error of the context, and says what it was doing for which
// host: connecting, the ssh handshake, the way on from a jump host, or
// starting the netconf subsystem.  When the transport is closed the
// underlying connection is closed too.
func DialHost(ctx context.Context, host string, opts ...DialOption) (*Transport, error) {
	return dialHost(ctx, host, processEnvironment(), opts...)
}

// dialHost is DialHost in the environment it is given.
func dialHost(ctx context.Context, host string, env environment, opts ...DialOption) (*Transport, error) {
	cfg := dialConfig{port: DefaultPort, env: env}
	for _, opt := range opts {
		opt.apply(&cfg)
	}

	var file *sshConfigFile
	if cfg.sshConfig != nil {
		var err error
		if file, err = cfg.loadSSHConfig(); err != nil {
			return nil, err
		}
	}

	// the port in the ssh config is not the device's, see WithSSHConfig
	hostname, _, err := cfg.applySSHConfig(file, host)
	if err != nil {
		return nil, err
	}

	var jump string
	if cfg.proxyJump != nil {
		jump = *cfg.proxyJump
	} else if jump, err = file.proxyJump(host); err != nil {
		return nil, err
	}
	hops, closeAgents, err := cfg.jumpHosts(file, jump)
	defer closeAgents()
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(hostname, strconv.Itoa(cfg.port))
	clientConfig, closeAgent, err := cfg.clientConfig(addr)
	if err != nil {
		return nil, err
	}
	defer closeAgent()

	// An agent that does not answer, one that waits for somebody to confirm
	// the use of a key, must not hold the dial past its context: the login
	// waits for the agent then, and not for the connection that the end of
	// the context closes.  Without its agent that login fails.
	stop := context.AfterFunc(ctx, func() {
		closeAgents()
		closeAgent()
	})
	defer stop()

	t, err := dial(ctx, "tcp", append(hops, hop{addr: addr, config: clientConfig, offered: &cfg.offered}), cfg.connect, cfg.command)
	if err != nil {
		return nil, err
	}

	if cfg.keepaliveInterval > 0 {
		go t.keepalive(cfg.keepaliveInterval, cfg.keepaliveMaxMissed)
	}
	return t, nil
}

// userName returns the OS user name, for a dial that was given no user.
func (env environment) userName() (string, error) {
	const unknown = "no user given and the current user is unknown"
	if env.user == nil {
		return "", errors.New(unknown)
	}
	name, err := env.user()
	if err != nil {
		return "", fmt.Errorf("%s: %w", unknown, err)
	}
	if name == "" {
		return "", errors.New(unknown)
	}
	return name, nil
}

// clientConfig builds what it takes to log in to the host at addr.  The
// returned function releases the agent connection and must be called once
// authentication is over.
func (cfg *dialConfig) clientConfig(addr string) (*ssh.ClientConfig, func(), error) {
	if cfg.user == "" {
		name, err := cfg.env.userName()
		if err != nil {
			return nil, nil, err
		}
		cfg.user = name
	}

	clientConfig := &ssh.ClientConfig{User: cfg.user}
	if err := cfg.hostKeyCheck(clientConfig, addr); err != nil {
		return nil, nil, err
	}

	auth, closeAgent, err := cfg.authMethods()
	if err != nil {
		return nil, nil, err
	}
	clientConfig.Auth = auth

	if cfg.tweak != nil {
		cfg.tweak(clientConfig)
	}
	return clientConfig, closeAgent, nil
}

// sshConfigFile is the ssh config file hosts are resolved through.  A nil
// file says nothing about any host.
type sshConfigFile struct {
	path string
	cfg  *ssh_config.Config
}

// loadSSHConfig reads the ssh config file named by WithSSHConfig.
func (cfg *dialConfig) loadSSHConfig() (*sshConfigFile, error) {
	path := *cfg.sshConfig
	if path == "" {
		var err error
		if path, err = cfg.env.expandHome("~/.ssh/config"); err != nil {
			return nil, err
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open ssh config: %w", err)
	}
	defer func() { _ = f.Close() }()

	sshCfg, err := ssh_config.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ssh config %s: %w", path, err)
	}
	return &sshConfigFile{path: path, cfg: sshCfg}, nil
}

// get returns the value of key that applies to host, empty when there is
// none.
func (f *sshConfigFile) get(host, key string) (string, error) {
	v, err := f.cfg.Get(host, key)
	if err != nil {
		return "", fmt.Errorf("ssh config %s: %w", f.path, err)
	}
	return v, nil
}

// proxyJump returns the jump hosts the file wants host to be reached
// through, as written, and nothing for a host that is reached directly.
func (f *sshConfigFile) proxyJump(host string) (string, error) {
	if f == nil {
		return "", nil
	}

	command, err := f.get(host, "ProxyCommand")
	if err != nil {
		return "", err
	}
	if command != "" && !strings.EqualFold(command, "none") {
		return "", fmt.Errorf("ssh config %s: host %s requires ProxyCommand which is not supported", f.path, host)
	}
	return f.get(host, "ProxyJump")
}

// applySSHConfig merges the settings for host from the ssh config file into
// cfg and returns the hostname to connect to and the port, which is 0 when
// the file names none.
func (cfg *dialConfig) applySSHConfig(file *sshConfigFile, host string) (hostname string, port int, err error) {
	if file == nil {
		return host, 0, nil
	}

	if hostname, err = file.get(host, "HostName"); err != nil {
		return "", 0, err
	}
	if hostname == "" {
		hostname = host
	}
	hostname = strings.ReplaceAll(hostname, "%h", host)

	portValue, err := file.get(host, "Port")
	if err != nil {
		return "", 0, err
	}
	if portValue != "" {
		if port, err = parsePort(portValue); err != nil {
			return "", 0, fmt.Errorf("ssh config %s: host %s: %w", file.path, host, err)
		}
	}

	if cfg.user == "" {
		if cfg.user, err = file.get(host, "User"); err != nil {
			return "", 0, err
		}
	}

	identities, err := file.cfg.GetAll(host, "IdentityFile")
	if err != nil {
		return "", 0, fmt.Errorf("ssh config %s: %w", file.path, err)
	}
	for _, identity := range identities {
		identity, err = cfg.env.expandHome(identity)
		if err != nil {
			return "", 0, err
		}
		cfg.identityFiles = append(cfg.identityFiles, keyFile{path: identity})
	}

	knownHosts, err := file.get(host, "UserKnownHostsFile")
	if err != nil {
		return "", 0, err
	}
	for known := range strings.FieldsSeq(knownHosts) {
		if known, err = cfg.env.expandHome(known); err != nil {
			return "", 0, err
		}
		cfg.sshKnownHosts = append(cfg.sshKnownHosts, known)
	}

	return hostname, port, nil
}

// hostKeyCheck configures host key verification for the connection to addr.
func (cfg *dialConfig) hostKeyCheck(clientConfig *ssh.ClientConfig, addr string) error {
	if cfg.pinned {
		given := cfg.hostKeys
		// Nothing to hold the device to is not a dial without a check: what
		// was said next to it, WithInsecureIgnoreHostKey, would decide then.
		if len(given) == 0 {
			return errors.New("WithHostKey was given no key to hold the device to")
		}
		if slices.Contains(given, nil) {
			return errors.New("WithHostKey was given a nil key")
		}

		clientConfig.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presented := key.Marshal()
			keyErr := &knownhosts.KeyError{}
			for _, want := range given {
				if bytes.Equal(presented, want.Marshal()) {
					return nil
				}
				keyErr.Want = append(keyErr.Want, knownhosts.KnownKey{Key: want})
			}
			return keyErr
		}

		types := make([]string, 0, len(given))
		for _, key := range given {
			types = append(types, key.Type())
		}
		clientConfig.HostKeyAlgorithms = hostKeyAlgorithms(types, false)
		return nil
	}
	if cfg.insecure {
		clientConfig.HostKeyCallback = ssh.InsecureIgnoreHostKey() //nolint:gosec // explicit opt-in via WithInsecureIgnoreHostKey
		return nil
	}

	files := cfg.knownHosts
	if len(files) == 0 && len(cfg.sshKnownHosts) > 0 {
		// like ssh, which does not mind a file on its list that is not there
		for _, f := range cfg.sshKnownHosts {
			if _, err := os.Stat(f); err == nil {
				files = append(files, f)
			}
		}
		if len(files) == 0 {
			return fmt.Errorf("failed to load known hosts (see WithKnownHosts): the ssh config names %s and none of it exists",
				strings.Join(cfg.sshKnownHosts, ", "))
		}
	}
	if len(files) == 0 {
		def, err := cfg.env.expandHome("~/.ssh/known_hosts")
		if err != nil {
			return err
		}
		files = []string{def}
	}

	known, err := knownhosts.New(files...)
	if err != nil {
		return fmt.Errorf("failed to load known hosts (see WithKnownHosts): %w", err)
	}

	// known_hosts files are mostly filled by logging in on port 22, so a
	// host unknown on the NETCONF port is looked up by its plain entry too.
	//
	// A host is looked up by the name it was dialed under.  The address of
	// the connection is not asked: knownhosts wants one it can read, and a
	// connection the caller made (WithDialer) may have none.
	callback := func(hostport string, _ net.Addr, key ssh.PublicKey) error {
		err := known(hostport, probeAddr(hostport), key)
		if fallback, ok := sshPortAddr(hostport); ok && isUnknownHost(err) {
			return known(fallback, probeAddr(fallback), key)
		}
		return err
	}
	clientConfig.HostKeyCallback = callback

	// Ask the server for the key types we have on file.  Otherwise a server
	// offering a different (unknown) type first fails verification even
	// though one of its keys is known.
	clientConfig.HostKeyAlgorithms = knownHostKeyAlgorithms(callback, addr)
	return nil
}

// sshPortAddr returns hostport with the regular SSH port, and false when it
// already uses it.
func sshPortAddr(hostport string) (string, bool) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port == "22" {
		return "", false
	}
	return net.JoinHostPort(host, "22"), true
}

// isUnknownHost reports whether err is known_hosts having nothing on the
// host (no key and no certificate authority), as opposed to a different or
// a revoked key.
func isUnknownHost(err error) bool {
	if err == nil {
		return false
	}
	var (
		keyErr     *knownhosts.KeyError
		revokedErr *knownhosts.RevokedError
	)
	switch {
	case errors.As(err, &revokedErr):
		return false
	case errors.As(err, &keyErr):
		return len(keyErr.Want) == 0
	default:
		return true
	}
}

type probeAddr string

func (probeAddr) Network() string  { return "tcp" }
func (a probeAddr) String() string { return string(a) }

type probeKey struct{}

func (probeKey) Type() string                        { return "netconf-probe" }
func (probeKey) Marshal() []byte                     { return []byte("netconf-probe") }
func (probeKey) Verify([]byte, *ssh.Signature) error { return errors.New("probe key") }

// knownHostKeyAlgorithms returns the host key algorithms matching the keys
// on file for addr, or nil when there are none.  Certificate variants are
// included because an entry may be a certificate authority.
func knownHostKeyAlgorithms(callback ssh.HostKeyCallback, addr string) []string {
	var keyErr *knownhosts.KeyError
	if err := callback(addr, probeAddr(addr), probeKey{}); !errors.As(err, &keyErr) {
		return nil
	}

	var types []string
	for _, known := range keyErr.Want {
		types = append(types, known.Key.Type())
	}
	return hostKeyAlgorithms(types, true)
}

// hostKeyAlgorithms returns the host key algorithms for keys of the given
// types, each once: for an RSA key, and for a certificate of one, the
// rsa-sha2 signatures in front of the old one, and with certs the
// certificate variants in front of each.
func hostKeyAlgorithms(types []string, certs bool) []string {
	var algos []string
	add := func(names ...string) {
		for _, name := range names {
			if !slices.Contains(algos, name) {
				algos = append(algos, name)
			}
		}
	}
	for _, t := range types {
		switch {
		case t == ssh.KeyAlgoRSA && certs:
			add(ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSAv01,
				ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		case t == ssh.KeyAlgoRSA:
			add(ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		case t == ssh.CertAlgoRSAv01:
			add(ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSAv01)
		case certs:
			add(t+"-cert-v01@openssh.com", t)
		default:
			add(t)
		}
	}
	return algos
}

// defaultIdentities are tried when neither the caller nor the ssh config
// names a key, like the OpenSSH client does.
var defaultIdentities = []string{"~/.ssh/id_ed25519", "~/.ssh/id_ecdsa", "~/.ssh/id_rsa"}

// authMethods builds the authentication methods in the order they are tried:
// key files, agent, password.  The returned function releases the agent
// connection and must be called once authentication is over.
func (cfg *dialConfig) authMethods() ([]ssh.AuthMethod, func(), error) {
	var (
		methods    []ssh.AuthMethod
		closeAgent = func() {}
	)

	// Keys the caller named must work; everything else is best effort.
	if slices.Contains(cfg.signers, nil) {
		return nil, nil, errors.New("WithSigner was given a nil signer")
	}
	signers := slices.Clone(cfg.signers)
	for _, kf := range cfg.keyFiles {
		signer, err := loadSigner(kf)
		if err != nil {
			return nil, nil, err
		}
		signers = append(signers, signer)
	}

	explicit := len(cfg.keyFiles) > 0 || cfg.namedSigners || cfg.password != nil
	if !explicit {
		identities := cfg.identityFiles
		if len(identities) == 0 {
			for _, path := range defaultIdentities {
				if path, err := cfg.env.expandHome(path); err == nil {
					identities = append(identities, keyFile{path: path})
				}
			}
		}
		for _, kf := range identities {
			// missing, or encrypted and thus expected to be in the agent
			if signer, err := loadSigner(kf); err == nil {
				signers = append(signers, signer)
			}
		}
	}
	sock := cfg.env.agentSock
	if cfg.agent && sock == "" {
		return nil, nil, errors.New("ssh agent requested but SSH_AUTH_SOCK is not set")
	}
	var agentSigners func() ([]ssh.Signer, error)
	if cfg.agent || (!explicit && sock != "") {
		conn, err := net.Dial("unix", sock) //nolint:noctx // local agent socket
		switch {
		case err == nil:
			closeAgent = func() { _ = conn.Close() }
			agentSigners = agent.NewClient(conn).Signers
		case cfg.agent:
			return nil, nil, fmt.Errorf("failed to connect to ssh agent: %w", err)
		}
	}

	// Key files and agent go into one method.  x/crypto/ssh tries every
	// kind of method once, so with two public key methods the keys of the
	// second are never offered.
	if len(signers) > 0 || agentSigners != nil {
		methods = append(methods, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			if agentSigners == nil {
				cfg.offered.keys.Store(int32(len(signers))) //nolint:gosec // a handful of keys
				return signers, nil
			}
			fromAgent, err := agentSigners()
			if err != nil && len(signers) == 0 {
				return nil, err
			}
			// an agent that fails must not take the key files with it
			all := append(slices.Clone(signers), fromAgent...)
			cfg.offered.keys.Store(int32(len(all))) //nolint:gosec // a handful of keys
			return all, nil
		}))
	}

	if cfg.password != nil {
		password := *cfg.password
		prompt := passwordPrompt(password)
		methods = append(methods,
			ssh.PasswordCallback(func() (string, error) {
				cfg.offered.password.Store(true)
				return password, nil
			}),
			// Devices behind RADIUS or TACACS+ often only offer
			// keyboard-interactive.
			ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				answers, err := prompt(name, instruction, questions, echos)
				if len(answers) > 0 {
					cfg.offered.interactive.Store(true)
				}
				return answers, err
			}),
		)
	}

	if len(methods) == 0 {
		if cfg.namedSigners {
			return nil, nil, errors.New("no ssh authentication method configured: WithSigner was given no signer")
		}
		return nil, nil, errors.New("no ssh authentication method configured")
	}
	return methods, closeAgent, nil
}

// passwordPrompt answers a keyboard-interactive exchange that asks for the
// password and nothing else: one hidden prompt, once.  Anything more (a
// second factor, an echoed field, a retry) is refused so the password is
// never typed into a prompt it was not meant for.
func passwordPrompt(password string) ssh.KeyboardInteractiveChallenge {
	answered := false
	return func(_, _ string, questions []string, echos []bool) ([]string, error) {
		switch {
		case len(questions) == 0:
			// informational round
			return nil, nil
		case len(questions) == 1 && !echos[0] && !answered:
			answered = true
			return []string{password}, nil
		default:
			return nil, errors.New("keyboard-interactive: server asks for more than the password")
		}
	}
}

func loadSigner(kf keyFile) (ssh.Signer, error) {
	pem, err := os.ReadFile(kf.path)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key: %w", err)
	}

	var signer ssh.Signer
	if kf.passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(kf.passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(pem)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key %s: %w", kf.path, err)
	}
	return signer, nil
}
