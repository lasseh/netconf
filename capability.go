package netconf

import (
	"iter"
	"strings"
)

const (
	baseCap      = "urn:ietf:params:netconf:base"
	stdCapPrefix = "urn:ietf:params:netconf:capability"
)

// The base capabilities name the protocol versions a peer speaks.  A session
// in which both peers advertise CapNetConf11 uses chunked framing.
const (
	CapNetConf10 = baseCap + ":1.0"
	CapNetConf11 = baseCap + ":1.1"
)

// DefaultCapabilities are the capabilities sent by the client during the hello
// exchange by the server.
var DefaultCapabilities = []string{
	CapNetConf10,
	CapNetConf11,
	// XXX: these seems like server capabilities and i don't see why
	// a client would need to send them
	// "urn:ietf:params:netconf:capability:writable-running:1.0",
	// "urn:ietf:params:netconf:capability:candidate:1.0",
	// "urn:ietf:params:netconf:capability:confirmed-commit:1.0",
	// "urn:ietf:params:netconf:capability:rollback-on-error:1.0",
	// "urn:ietf:params:netconf:capability:startup:1.0",
	// "urn:ietf:params:netconf:capability:url:1.0?scheme=http,ftp,file,https,sftp",
	// "urn:ietf:params:netconf:capability:validate:1.0",
	// "urn:ietf:params:netconf:capability:xpath:1.0",
	// "urn:ietf:params:netconf:capability:notification:1.0",
	// "urn:ietf:params:netconf:capability:interleave:1.0",
	// "urn:ietf:params:netconf:capability:with-defaults:1.0",
}

// expandCapability adds the standard capability prefix,
// `urn:ietf:params:netconf:capability`, to a capability given in short form.
func expandCapability(s string) string {
	if s == "" {
		return ""
	}

	if s[0] != ':' {
		return s
	}

	return stdCapPrefix + s
}

// CapabilitySet is the set of capabilities advertised by one peer.
type CapabilitySet struct {
	caps map[string]struct{}
	// uris holds every capability without its parameters, and also without
	// its version, so a capability can be found without knowing the values
	// the peer attached to it or the version it speaks
	uris map[string]struct{}
}

// cutVersion returns uri without a version at its end: ":validate" for
// ":validate:1.1".  A uri that does not end in a version is returned as it
// is, with false.
func cutVersion(uri string) (string, bool) {
	i := strings.LastIndexByte(uri, ':')
	if i < 0 {
		return uri, false
	}
	version := uri[i+1:]
	if version == "" || version[0] < '0' || version[0] > '9' ||
		strings.Trim(version, "0123456789.") != "" {
		return uri, false
	}
	return uri[:i], true
}

// NewCapabilitySet creates a new CapabilitySet and adds any provided
// capabilities.
func NewCapabilitySet(capabilities ...string) CapabilitySet {
	cs := CapabilitySet{
		caps: make(map[string]struct{}),
		uris: make(map[string]struct{}),
	}
	for _, c := range capabilities {
		c = expandCapability(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		cs.caps[c] = struct{}{}
		uri, _, _ := strings.Cut(c, "?")
		cs.uris[uri] = struct{}{}
		if bare, ok := cutVersion(uri); ok {
			cs.uris[bare] = struct{}{}
		}
	}
	return cs
}

// Len returns the number of capabilities in the set.
func (cs CapabilitySet) Len() int {
	return len(cs.caps)
}

// All returns an iterator over all capabilities in the set.  If you want a
// slice use `slices.Collect(cs.All())`.
func (cs CapabilitySet) All() iter.Seq[string] {
	return func(yield func(string) bool) {
		for c := range cs.caps {
			if !yield(c) {
				return
			}
		}
	}
}

// Has will return true if the capability is present in the set.  The given
// capability may be given in short form, without the standard prefix:
// ":candidate:1.0".
//
// A capability given without a version, ":validate", matches any version
// the peer advertised, ":validate:1.0" or ":validate:1.1"; one given with a
// version matches that version only.  The same goes for the base
// capability: "urn:ietf:params:netconf:base" matches either base version.
//
// Capabilities may carry parameters (for example
// ":url:1.0?scheme=http,ftp,file").  A capability given without parameters
// matches whatever parameters the peer advertised; one given with parameters
// must match exactly.
func (cs CapabilitySet) Has(s string) bool {
	s = expandCapability(s)
	if strings.Contains(s, "?") {
		_, ok := cs.caps[s]
		return ok
	}
	_, ok := cs.uris[s]
	return ok
}
