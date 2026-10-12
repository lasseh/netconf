package tls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
)

// testServer handles the boilerplate of a one-shot TLS server.
type testServer struct {
	t        *testing.T
	listener net.Listener
	config   *tls.Config
	errCh    chan error
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()

	// 1. Generate ephemeral self-signed cert
	cert, err := generateSelfSignedCert()
	be.NilErr(t, err)

	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "localhost:0")
	be.NilErr(t, err)

	return &testServer{
		t:        t,
		listener: ln,
		config:   config,
		errCh:    make(chan error, 1),
	}
}

func (s *testServer) Addr() string {
	return s.listener.Addr().String()
}

// Serve accepts ONE connection, performs the TLS handshake,
// and hands the connection to the handler.
func (s *testServer) Serve(handler func(net.Conn) error) {
	go func() {
		defer close(s.errCh)
		defer func() {
			if err := s.listener.Close(); err != nil {
				s.t.Logf("testServer listener close: %v", err)
			}
		}()

		conn, err := s.listener.Accept()
		if err != nil {
			s.errCh <- fmt.Errorf("accept: %w", err)
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				s.t.Logf("testServer conn close: %v", err)
			}
		}()

		tlsConn := tls.Server(conn, s.config)

		// Force handshake to ensure the connection is actually established
		// before handing it off.
		if err := tlsConn.HandshakeContext(context.Background()); err != nil {
			s.errCh <- fmt.Errorf("handshake: %w", err)
			return
		}

		if err := handler(tlsConn); err != nil {
			s.errCh <- err
		}
	}()
}

func (s *testServer) Wait(t *testing.T) {
	t.Helper()
	err := <-s.errCh
	be.NilErr(t, err)
}

func TestTransport_DialContextCancel(t *testing.T) {
	// Raw TCP listener that accepts but never handshakes
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "localhost:0")
	be.NilErr(t, err)
	defer func() {
		if err := ln.Close(); err != nil {
			t.Logf("failed to close listener: %v", err)
		}
	}()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			_, _ = io.Copy(io.Discard, conn)
		}
	}()

	config := &tls.Config{InsecureSkipVerify: true}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = Dial(ctx, "tcp", ln.Addr().String(), config)

	be.True(t, errors.Is(err, context.DeadlineExceeded))
}

// generateSelfSignedCert creates an in-memory generic cert for testing.
func generateSelfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Acme Co"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Hour),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}

	return tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  key,
	}, nil
}
