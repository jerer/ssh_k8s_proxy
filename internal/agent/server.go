package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Options configures the in-container SSH agent.
type Options struct {
	HostKeyPath           string
	Shell                 string
	WorkDir               string
	Auth                  AuthOptions
	ServerVersion         string
	Logger                *log.Logger
	SuppressNoAuthWarning bool
	RequireAuth           bool
}

// Server is an SSH server that executes commands locally. In stdio mode it is
// connected to a Kubernetes exec stream; in listen mode it can run as a service
// inside the container.
type Server struct {
	config *ssh.ServerConfig
	opts   Options
	logger *log.Logger
}

// NewServer builds an SSH server, creating or loading its host key.
func NewServer(opts Options) (*Server, error) {
	logger := opts.Logger
	if logger == nil {
		logger = log.New(os.Stderr, "kubessh: ", log.LstdFlags)
	}

	warnf := func(format string, args ...any) {
		logger.Printf("warning: "+format, args...)
	}

	hostKey, err := loadOrCreateHostKey(opts.HostKeyPath, warnf)
	if err != nil {
		return nil, err
	}

	auth, err := buildAuth(opts.Auth)
	if err != nil {
		return nil, err
	}

	serverVersion := opts.ServerVersion
	if serverVersion == "" {
		serverVersion = "SSH-2.0-kubessh"
	}

	config := &ssh.ServerConfig{
		ServerVersion: serverVersion,
		MaxAuthTries:  6,
	}
	config.AddHostKey(hostKey)

	switch {
	case len(auth.keys) == 0 && auth.password == "":
		if opts.RequireAuth && !opts.Auth.AllowNone {
			return nil, fmt.Errorf("no SSH credentials configured; use --authorized-key/--authorized-keys or --password, or pass --allow-no-auth")
		}
		config.NoClientAuth = true
		if !opts.SuppressNoAuthWarning {
			logger.Printf("warning: no SSH credentials configured; accepting unauthenticated connections (Kubernetes RBAC is the access boundary)")
		}
	default:
		if len(auth.keys) > 0 {
			config.PublicKeyCallback = func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
				if keyAllowed(auth.keys, key) {
					return nil, nil
				}
				return nil, fmt.Errorf("public key not authorized")
			}
		}
		if auth.password != "" {
			config.PasswordCallback = func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
				if passwordMatches(auth.password, string(password)) {
					return nil, nil
				}
				return nil, fmt.Errorf("invalid password")
			}
		}
	}

	if opts.Shell == "" {
		opts.Shell = defaultShell()
	}

	return &Server{config: config, opts: opts, logger: logger}, nil
}

// ServeConn serves a single SSH connection until it is closed.
func (s *Server) ServeConn(conn net.Conn) error {
	serverConn, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return err
	}
	defer serverConn.Close()

	go handleGlobalRequests(reqs)

	var wg sync.WaitGroup
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			s.logger.Printf("accept session channel: %v", err)
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleSession(channel, requests)
		}()
	}
	wg.Wait()
	return nil
}

// ListenAndServe starts a TCP SSH server and blocks until ctx is canceled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer listener.Close()

	s.logger.Printf("listening on %s", listener.Addr())

	var connections sync.Map

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
			connections.Range(func(key, _ any) bool {
				_ = key.(net.Conn).Close()
				return true
			})
		case <-done:
		}
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}

		connections.Store(conn, struct{}{})
		go func() {
			defer connections.Delete(conn)
			if err := s.ServeConn(conn); err != nil && !isClosedError(err) {
				s.logger.Printf("connection from %s closed: %v", conn.RemoteAddr(), err)
			}
			_ = conn.Close()
		}()
	}
}

func handleGlobalRequests(requests <-chan *ssh.Request) {
	for request := range requests {
		reply := false
		switch request.Type {
		case "keepalive@openssh.com", "no-more-sessions@openssh.com":
			reply = true
		}
		if request.WantReply {
			_ = request.Reply(reply, nil)
		}
	}
}

func defaultShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/bash"
}

func isClosedError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) || errors.Is(err, io.EOF) {
		return true
	}
	return strings.Contains(err.Error(), "closed network connection") ||
		strings.Contains(err.Error(), "EOF")
}
