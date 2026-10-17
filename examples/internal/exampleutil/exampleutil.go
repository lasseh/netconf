// Package exampleutil owns SSH setup and shutdown for the runnable examples.
// It is example code, not part of the library's public API.
package exampleutil

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lasseh/netconf"
	ncssh "github.com/lasseh/netconf/transport/ssh"
)

const (
	connectTimeout   = 30 * time.Second
	operationTimeout = 30 * time.Second
	closeTimeout     = 5 * time.Second
	keepalive        = 15 * time.Second
)

// Main parses the common SSH flags, opens a session, runs the use case and
// closes the session. It exits with status 1 if setup, the use case or cleanup
// fails. Callers register any use-case flags before calling Main.
func Main(useCase func(context.Context, *netconf.Session) error, opts ...netconf.SessionOption) {
	host := flag.String("host", "", "device hostname, required")
	user := flag.String("user", "", "SSH username, default from the selected SSH config then the current user")
	port := flag.Int("port", 830, "NETCONF SSH port")
	sshConfig := flag.String("ssh-config", "", "optional SSH config file for aliases and jump hosts")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *host, *user, *port, *sshConfig, useCase, opts)
	stop()
	if err != nil {
		log.Print(err)
		// run has already closed the session, so os.Exit skips no I/O cleanup.
		os.Exit(1)
	}
}

func run(ctx context.Context, host, user string, port int, sshConfig string, useCase func(context.Context, *netconf.Session) error, opts []netconf.SessionOption) (err error) {
	if host == "" {
		return errors.New("-host is required; use -help for usage")
	}
	sshOpts := []ncssh.DialOption{ncssh.WithKeepalive(keepalive, 3), ncssh.WithPort(port)}
	if user != "" {
		sshOpts = append(sshOpts, ncssh.WithUser(user))
	}
	if sshConfig != "" {
		sshOpts = append(sshOpts, ncssh.WithSSHConfig(sshConfig))
	}

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	tr, err := ncssh.DialHost(connectCtx, host, sshOpts...)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	opts = append([]netconf.SessionOption{netconf.WithOperationTimeout(operationTimeout)}, opts...)
	session, err := netconf.Open(connectCtx, tr, opts...)
	if err != nil {
		// Open owns the transport and closes it on failure too.
		return fmt.Errorf("open: %w", err)
	}
	cancel() // the setup context does not own the opened session
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		if closeErr := session.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close: %w", closeErr))
		}
	}()

	return useCase(ctx, session)
}
