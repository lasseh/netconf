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
