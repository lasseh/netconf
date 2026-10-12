package ssh

import (
	"errors"
	"os"
	"os/user"
	"reflect"
	"testing"

	"github.com/carlmjohnson/be"
	"golang.org/x/crypto/ssh"
)

// What DialHost takes from the process it runs in: the home directory and
// the socket of the agent as they are, the user only when it is asked for.
func TestProcessEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "/run/agent.sock")

	env := processEnvironment()
	be.Equal(t, home, env.home)
	be.Equal(t, "/run/agent.sock", env.agentSock)

	current, err := user.Current()
	be.NilErr(t, err)
	name, err := env.userName()
	be.NilErr(t, err)
	be.Equal(t, current.Username, name)

	// and nobody has been looked up before somebody asks
	be.Equal(t, reflect.ValueOf(currentUser).Pointer(), reflect.ValueOf(env.user).Pointer())
}

// A process without a home directory is told why it has none, by whatever
// could not find one.
func TestDialHost_NoHome(t *testing.T) {
	t.Setenv("HOME", "")
	_, why := os.UserHomeDir()
	be.Nonzero(t, why)

	_, err := DialHost(t.Context(), "127.0.0.1", WithUser("admin"), WithPassword("secret"))
	be.Nonzero(t, err)
	be.Equal(t, "failed to expand ~/.ssh/known_hosts: "+why.Error(), err.Error())
}

// The user the process runs as is the user to log in as when nobody names
// one.  That takes a lookup, which is only made when it is needed, and what
// it fails with is in the error.
func TestDialHost_CurrentUser(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		lookup := errors.New("user: unknown userid 4711")
		env := environment{home: t.TempDir(), user: func() (string, error) { return "", lookup }}

		_, err := dialHost(t.Context(), "127.0.0.1", env, WithPassword("secret"), WithInsecureIgnoreHostKey())
		be.Equal(t, "no user given and the current user is unknown: user: unknown userid 4711", err.Error())
		be.True(t, errors.Is(err, lookup))
	})

	t.Run("without a name", func(t *testing.T) {
		for _, env := range []environment{
			{home: t.TempDir()},
			{home: t.TempDir(), user: func() (string, error) { return "", nil }},
		} {
			_, err := dialHost(t.Context(), "127.0.0.1", env, WithPassword("secret"), WithInsecureIgnoreHostKey())
			be.Equal(t, "no user given and the current user is unknown", err.Error())
		}
	})

	t.Run("not asked for when a user is given", func(t *testing.T) {
		env := environment{home: t.TempDir(), user: func() (string, error) {
			t.Error("the current user was looked up")
			return "", nil
		}}

		// nothing listens there: the dial gets as far as connecting
		_, err := dialHost(t.Context(), "127.0.0.1", env,
			WithPort(1), WithUser("admin"), WithPassword("secret"), WithInsecureIgnoreHostKey())
		be.In(t, "connection refused", err.Error())
	})

	t.Run("asked for when nobody names one", func(t *testing.T) {
		srv := newTestServer(t)
		srv.config.NoClientAuth = false
		var loggedIn string
		srv.config.PasswordCallback = func(conn ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			loggedIn = conn.User()
			return nil, nil
		}
		srv.Serve(greet)

		host, port := hostPort(t, srv.Addr())
		env := environment{home: t.TempDir(), user: func() (string, error) { return "whoever", nil }}
		tr, err := dialHost(t.Context(), host, env, WithPort(port), WithPassword("secret"), WithInsecureIgnoreHostKey())
		be.NilErr(t, err)
		readGreeting(t, tr)
		be.NilErr(t, tr.Close())
		be.Equal(t, "whoever", loggedIn)
	})
}
