//go:build !windows

package agent

import (
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func startTestServer(t *testing.T) (*ssh.Client, func()) {
	t.Helper()
	t.Setenv("KUBESSH_AUTHORIZED_KEYS", "")
	t.Setenv("KUBESSH_AUTHORIZED_KEYS_FILES", "")
	t.Setenv("KUBESSH_PASSWORD", "")

	emptyKeys := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(emptyKeys, nil, 0o600); err != nil {
		t.Fatalf("write empty authorized keys: %v", err)
	}

	server, err := NewServer(Options{
		HostKeyPath:           filepath.Join(t.TempDir(), "hostkey"),
		Shell:                 "/bin/sh",
		WorkDir:               t.TempDir(),
		Logger:                log.New(io.Discard, "", 0),
		SuppressNoAuthWarning: true,
		Auth: AuthOptions{
			AuthorizedKeysFiles: []string{emptyKeys},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- server.ServeConn(conn)
		_ = conn.Close()
	}()

	clientConn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	clientConnTyped, chans, reqs, err := ssh.NewClientConn(clientConn, listener.Addr().String(), &ssh.ClientConfig{
		User:            "tester",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	client := ssh.NewClient(clientConnTyped, chans, reqs)

	cleanup := func() {
		_ = client.Close()
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	}
	return client, cleanup
}

func TestServerExec(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer session.Close()

	output, err := session.Output("printf hello")
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	if string(output) != "hello" {
		t.Fatalf("output = %q, want %q", output, "hello")
	}
}

func TestServerExitStatus(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer session.Close()

	err = session.Run("exit 7")
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitStatus() != 7 {
		t.Fatalf("exit status = %d, want 7", exitErr.ExitStatus())
	}
}

func TestServerEnv(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer session.Close()

	if err := session.Setenv("KUBESSH_TEST_VALUE", "from-env"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	output, err := session.Output("printf %s \"$KUBESSH_TEST_VALUE\"")
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	if string(output) != "from-env" {
		t.Fatalf("output = %q, want %q", output, "from-env")
	}
}
