// Package tls is the NETCONF over TLS transport of RFC 7589.
//
// The package name collides with crypto/tls, so it is usually imported under
// another name:
//
//	import nctls "github.com/lasseh/netconf/transport/tls"
package tls

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/lasseh/netconf/transport"
)

// alias it to a private type so we can make it private when embedding.
type framer = transport.Framer

// Transport implements RFC7589 for implementing NETCONF over TLS.
type Transport struct {
	conn *tls.Conn
	*framer
}

// Dial will connect to a NETCONF tls port and creates a new Transport.  It's
// used as a convenience function and essentially is the same as:
//
//	c, err := tls.Dial(network, addr, config)
//	if err != nil { /* ... handle error ... */ }
//	t, err := NewTransport(c)
//
// When the transport is closed the underlying connection is also closed.
func Dial(ctx context.Context, network, addr string, config *tls.Config) (*Transport, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}

	// Like tls.Dial, take the host being dialed for the name unless the caller
	// chose one.  That goes for InsecureSkipVerify as well: a verifier that
	// replaces the default check finds the name on the connection.  Only the
	// default check cannot do without one.
	if config == nil {
		config = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if config.ServerName == "" {
		host, _, err := net.SplitHostPort(addr)
		switch {
		case err == nil:
			config = config.Clone()
			config.ServerName = host
		case !config.InsecureSkipVerify:
			_ = conn.Close()
			return nil, err
		}
	}

	tlsConn := tls.Client(conn, config)

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}

	return NewTransport(tlsConn), nil
}

// NewTransport takes an already connected tls transport and returns a new
// Transport.
func NewTransport(conn *tls.Conn) *Transport {
	return &Transport{
		conn:   conn,
		framer: transport.NewFramer(conn, conn),
	}
}

// closeWait is how long Close gives close_notify before it closes the socket.
const closeWait = time.Second

// Close will close the transport and the underlying TLS connection.
//
// tls.Conn.Close takes the write lock for its close_notify before it sets a
// deadline, and a read that answers a device's KeyUpdate holds that lock through
// a socket write with no deadline.  Close is what cancels everything else, so it
// must not wait for a device that stopped reading: the socket is closed under a
// timer, which ends that write.
func (t *Transport) Close() error {
	timer := time.AfterFunc(closeWait, func() { _ = t.conn.NetConn().Close() })
	defer timer.Stop()
	return t.conn.Close()
}
