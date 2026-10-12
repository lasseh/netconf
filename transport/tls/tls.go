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

	// Like tls.Dial, verify the certificate against the host being dialed
	// unless the caller chose a name.
	if config == nil {
		config = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if config.ServerName == "" && !config.InsecureSkipVerify {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		config = config.Clone()
		config.ServerName = host
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

// Close will close the transport and the underlying TLS connection.
func (t *Transport) Close() error {
	return t.conn.Close()
}
