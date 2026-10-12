package ssh

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// jumpPort is the port a jump host is logged in on when nothing names one.
const jumpPort = 22

// jumpSpec is a jump host as written in a ProxyJump list.
type jumpSpec struct {
	user string
	host string
	port int // 0 when none is written
}

// parseProxyJump parses a ProxyJump list as ssh takes it: jump hosts
// separated by commas, each [user@]host[:port] or an ssh:// URI.  An empty
// list and "none" are no jump host.
func parseProxyJump(value string) ([]jumpSpec, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "none") {
		return nil, nil
	}

	var specs []jumpSpec
	for entry := range strings.SplitSeq(value, ",") {
		spec, err := parseJumpHost(strings.TrimSpace(entry))
		if err != nil {
			return nil, fmt.Errorf("invalid jump host %q in %q: %w", entry, value, err)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func parseJumpHost(entry string) (jumpSpec, error) {
	var spec jumpSpec

	hostport := entry
	if rest, ok := strings.CutPrefix(entry, "ssh://"); ok {
		u, err := url.Parse(entry)
		if err != nil {
			return spec, err
		}
		if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
			return spec, errors.New("more than a host in the ssh URI")
		}
		spec.user = u.User.Username()
		hostport = u.Host
		if strings.Contains(rest, "@") && spec.user == "" {
			return spec, errors.New("empty user")
		}
	} else if i := strings.LastIndex(entry, "@"); i >= 0 {
		if spec.user, hostport = entry[:i], entry[i+1:]; spec.user == "" {
			return spec, errors.New("empty user")
		}
	}

	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// no port: a name, or an IPv6 address with or without brackets
		spec.host = strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	} else {
		spec.host = host
		if spec.port, err = parsePort(port); err != nil {
			return spec, err
		}
	}
	if spec.host == "" {
		return spec, errors.New("empty host")
	}
	return spec, nil
}

func parsePort(s string) (int, error) {
	port, err := strconv.Atoi(s)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return port, nil
}

// jumpHosts resolves a ProxyJump list into the hops to log in to, in the
// order they are dialed.  The returned function is never nil: it releases
// the agent connections and must be called once the dial is over.
func (cfg *dialConfig) jumpHosts(file *sshConfigFile, list string) ([]hop, func(), error) {
	var closers []func()
	closeAgents := func() {
		for _, closeAgent := range closers {
			closeAgent()
		}
	}

	specs, err := parseProxyJump(list)
	if err != nil {
		return nil, closeAgents, err
	}

	var hops []hop
	for i, spec := range specs {
		// What the caller said about the device stays with the device: a
		// jump host starts from nothing but where to find keys and known
		// hosts.
		jump := dialConfig{
			env:        cfg.env,
			user:       spec.user,
			agent:      cfg.agent,
			knownHosts: cfg.knownHosts,
		}

		hostname, port, err := jump.applySSHConfig(file, spec.host)
		if err != nil {
			return nil, closeAgents, err
		}
		if spec.port != 0 {
			port = spec.port
		}
		if port == 0 {
			port = jumpPort
		}

		// Only the first of the list is reached the way the ssh config
		// says.  The others are reached through the one before them.
		if i == 0 {
			nested, err := file.proxyJump(spec.host)
			if err != nil {
				return nil, closeAgents, err
			}
			if further, err := parseProxyJump(nested); err != nil || len(further) > 0 {
				return nil, closeAgents, fmt.Errorf(
					"jump host %s has a ProxyJump of its own (%s), which is not followed: name every jump host in order",
					spec.host, nested)
			}
		}

		addr := net.JoinHostPort(hostname, strconv.Itoa(port))
		config, closeAgent, err := jump.clientConfig(addr)
		if err != nil {
			return nil, closeAgents, fmt.Errorf("jump host %s: %w", spec.host, err)
		}
		closers = append(closers, closeAgent)
		hops = append(hops, hop{jump: spec.host, addr: addr, config: config, offered: &jump.offered})
	}
	return hops, closeAgents, nil
}
