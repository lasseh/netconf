package netconf_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/rpc"
	nctls "github.com/lasseh/netconf/transport/tls"
)

const tlsAddr = "myrouter.example.com:6513"

func Example_tls() {
	caCert, err := os.ReadFile("ca.crt")
	if err != nil {
		log.Fatalf("failed to load ca cert: %v", err)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	clientCert, err := os.ReadFile("client.crt")
	if err != nil {
		log.Fatalf("failed to load client cert: %v", err)
	}

	clientKey, err := os.ReadFile("client.key")
	if err != nil {
		log.Fatalf("failed to load client key: %v", err)
	}

	cert, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		panic(err)
	}

	// tls transport configuration
	// The server certificate is verified against the CA for the host name
	// being dialed.
	config := tls.Config{
		RootCAs:      caCertPool,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// Five seconds to connect and open the session.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport, err := nctls.Dial(ctx, "tcp", tlsAddr, &config)
	if err != nil {
		panic(err)
	}

	session, err := netconf.Open(ctx, transport)
	if err != nil {
		panic(err)
	}

	// timeout for the call itself.
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg, err := rpc.GetConfig{Source: rpc.Running}.Exec(ctx, session)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Config: %s\n", cfg)

	if err := session.Close(context.Background()); err != nil {
		log.Print(err)
	}
}
