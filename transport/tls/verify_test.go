package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
)

// certFor returns a self-signed certificate valid for the given IPs and a
// pool trusting it.
func certFor(t *testing.T, ips ...net.IP) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	be.NilErr(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "netconf test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	be.NilErr(t, err)

	leaf, err := x509.ParseCertificate(der)
	be.NilErr(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// Dial must verify the server certificate against the dialed host without
// the caller having to repeat the name in the config.
func TestDialVerifiesDialedHost(t *testing.T) {
	serve := func(t *testing.T, cert tls.Certificate) string {
		t.Helper()
		srv := newTestServer(t)
		srv.config.Certificates = []tls.Certificate{cert}
		srv.Serve(func(c net.Conn) error {
			_, _ = io.WriteString(c, "hi]]>]]>")
			_, _ = io.Copy(io.Discard, c)
			return nil
		})
		return srv.Addr()
	}

	t.Run("certificate for the host", func(t *testing.T) {
		cert, pool := certFor(t, net.IPv4(127, 0, 0, 1), net.IPv6loopback)
		tr, err := Dial(t.Context(), "tcp", serve(t, cert), &tls.Config{RootCAs: pool})
		be.NilErr(t, err)
		be.NilErr(t, tr.Close())
	})

	t.Run("certificate for another host", func(t *testing.T) {
		cert, pool := certFor(t, net.IPv4(192, 0, 2, 1))
		_, err := Dial(t.Context(), "tcp", serve(t, cert), &tls.Config{RootCAs: pool})
		var hostErr x509.HostnameError
		be.True(t, errors.As(err, &hostErr))
	})
}

// certForName returns a self-signed certificate for a DNS name and a pool
// trusting it.
func certForName(t *testing.T, name string) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	be.NilErr(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "netconf test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{name},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	be.NilErr(t, err)
	leaf, err := x509.ParseCertificate(der)
	be.NilErr(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// A verifier that replaces the default check is written the way crypto/tls shows
// it: it takes the name from the connection.  Dial has to have put it there.
func TestDialServerNameUnderInsecureSkipVerify(t *testing.T) {
	dial := func(t *testing.T, cert tls.Certificate, pool *x509.CertPool, seen *string) error {
		t.Helper()
		srv := newTestServer(t)
		srv.config.Certificates = []tls.Certificate{cert}
		srv.Serve(func(c net.Conn) error {
			_, _ = io.Copy(io.Discard, c)
			return nil
		})
		_, port, err := net.SplitHostPort(srv.Addr())
		be.NilErr(t, err)

		tr, err := Dial(t.Context(), "tcp", net.JoinHostPort("localhost", port), &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // replaced by VerifyConnection
			VerifyConnection: func(cs tls.ConnectionState) error {
				*seen = cs.ServerName
				_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: cs.ServerName, Roots: pool})
				return err
			},
		})
		if err == nil {
			_ = tr.Close()
		}
		return err
	}

	t.Run("a certificate for the host", func(t *testing.T) {
		var seen string
		cert, pool := certForName(t, "localhost")
		be.NilErr(t, dial(t, cert, pool, &seen))
		be.Equal(t, "localhost", seen)
	})

	t.Run("a certificate for another name", func(t *testing.T) {
		var seen string
		cert, pool := certForName(t, "other.example.net")
		be.True(t, dial(t, cert, pool, &seen) != nil)
		be.Equal(t, "localhost", seen)
	})
}

// An address with no port is the one thing that keeps a name from being found,
// and only the default check needs one.
func TestDialAddressWithoutAPort(t *testing.T) {
	srv := newTestServer(t)
	srv.Serve(func(c net.Conn) error {
		_, _ = io.Copy(io.Discard, c)
		return nil
	})

	// a network whose addresses have no port
	path := t.TempDir() + "/s"
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	be.NilErr(t, err)
	defer func() { _ = ln.Close() }()

	cert, err := generateSelfSignedCert()
	be.NilErr(t, err)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		_, _ = io.Copy(io.Discard, s)
	}()

	tr, err := Dial(t.Context(), "unix", path, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // a test device
	be.NilErr(t, err)
	be.NilErr(t, tr.Close())

	_, err = Dial(t.Context(), "unix", path, &tls.Config{MinVersion: tls.VersionTLS12})
	be.True(t, err != nil)
}
