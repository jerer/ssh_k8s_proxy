//go:build !windows

package agent

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

type ptyRequestMsg struct {
	Term    string
	Columns uint32
	Rows    uint32
	Width   uint32
	Height  uint32
	Modes   string
}

type windowChangeMsg struct {
	Columns uint32
	Rows    uint32
	Width   uint32
	Height  uint32
}

type envMsg struct {
	Name  string
	Value string
}

type execMsg struct {
	Command string
}

type signalMsg struct {
	Signal string
}

type exitStatusMsg struct {
	Status uint32
}

type session struct {
	channel ssh.Channel
	opts    Options
	logger  *log.Logger

	mu       sync.Mutex
	env      []string
	pty      *ptyRequestMsg
	cmd      *exec.Cmd
	ptmx     *os.File
	started  bool
	outDone  chan struct{}
	waitDone chan struct{}
	exited   atomic.Bool
	finished sync.Once
}

func (s *Server) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	sess := &session{
		channel: channel,
		opts:    s.opts,
		logger:  s.logger,
	}

	defer func() {
		sess.kill()
		sess.waitForExit(2 * time.Second)
		sess.finished.Do(func() { _ = channel.Close() })
	}()

	for request := range requests {
		switch request.Type {
		case "env":
			var message envMsg
			if err := ssh.Unmarshal(request.Payload, &message); err != nil {
				request.Reply(false, nil)
				continue
			}
			if strings.ContainsRune(message.Name, '=') || message.Name == "" {
				request.Reply(false, nil)
				continue
			}
			sess.mu.Lock()
			sess.env = append(sess.env, message.Name+"="+message.Value)
			sess.mu.Unlock()
			request.Reply(true, nil)

		case "pty-req":
			var message ptyRequestMsg
			if err := ssh.Unmarshal(request.Payload, &message); err != nil {
				request.Reply(false, nil)
				continue
			}
			sess.mu.Lock()
			sess.pty = &message
			sess.mu.Unlock()
			request.Reply(true, nil)

		case "window-change":
			var message windowChangeMsg
			if err := ssh.Unmarshal(request.Payload, &message); err != nil {
				request.Reply(false, nil)
				continue
			}
			sess.resize(message)
			request.Reply(true, nil)

		case "shell":
			if err := sess.start(request.Type, nil); err != nil {
				sess.logger.Printf("start shell: %v", err)
				request.Reply(false, nil)
				sess.finish(1)
				return
			}
			request.Reply(true, nil)

		case "exec":
			var message execMsg
			if err := ssh.Unmarshal(request.Payload, &message); err != nil {
				request.Reply(false, nil)
				continue
			}
			if err := sess.start(request.Type, []byte(message.Command)); err != nil {
				sess.logger.Printf("start exec: %v", err)
				request.Reply(false, nil)
				sess.finish(1)
				return
			}
			request.Reply(true, nil)

		case "signal":
			var message signalMsg
			if err := ssh.Unmarshal(request.Payload, &message); err == nil {
				sess.sendSignal(message.Signal)
			}
			request.Reply(true, nil)

		default:
			request.Reply(false, nil)
		}
	}
}

func (s *session) start(requestType string, payload []byte) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("session already started")
	}
	s.started = true
	ptyRequest := s.pty
	env := s.environmentLocked(ptyRequest)
	s.mu.Unlock()

	shell := s.opts.Shell
	if shell == "" {
		shell = defaultShell()
	}

	var argv []string
	switch requestType {
	case "shell":
		argv = []string{shell, "-l"}
	case "exec":
		argv = []string{shell, "-c", string(payload)}
	default:
		return fmt.Errorf("unsupported session request %q", requestType)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = s.workDir()

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()

	if ptyRequest != nil {
		size := &pty.Winsize{
			Rows: uint16(ptyRequest.Rows),
			Cols: uint16(ptyRequest.Columns),
			X:    uint16(ptyRequest.Width),
			Y:    uint16(ptyRequest.Height),
		}
		ptmx, err := pty.StartWithSize(cmd, size)
		if err != nil {
			return fmt.Errorf("start pty: %w", err)
		}
		outDone := make(chan struct{})

		s.mu.Lock()
		s.ptmx = ptmx
		s.outDone = outDone
		s.mu.Unlock()

		go func() {
			defer close(outDone)
			_, _ = io.Copy(s.channel, ptmx)
		}()
		go func() {
			_, _ = io.Copy(ptmx, s.channel)
		}()
	} else {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("create stdin pipe: %w", err)
		}
		cmd.Stdout = s.channel
		cmd.Stderr = s.channel.Stderr()
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start command: %w", err)
		}
		go func() {
			_, _ = io.Copy(stdin, s.channel)
			_ = stdin.Close()
		}()
	}

	s.mu.Lock()
	s.waitDone = make(chan struct{})
	s.mu.Unlock()

	go s.wait()
	return nil
}

func (s *session) wait() {
	err := s.cmd.Wait()
	s.exited.Store(true)

	s.mu.Lock()
	ptmx := s.ptmx
	outDone := s.outDone
	s.mu.Unlock()

	if ptmx != nil {
		_ = ptmx.Close()
		if outDone != nil {
			<-outDone
		}
	}

	s.finish(exitStatus(err))

	s.mu.Lock()
	waitDone := s.waitDone
	s.mu.Unlock()
	if waitDone != nil {
		close(waitDone)
	}
}

func (s *session) finish(status uint32) {
	s.finished.Do(func() {
		payload := ssh.Marshal(exitStatusMsg{Status: status})
		_, _ = s.channel.SendRequest("exit-status", false, payload)
		_ = s.channel.Close()
	})
}

func (s *session) kill() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil || s.exited.Load() {
		return
	}

	pid := cmd.Process.Pid
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 1 && pgid != syscall.Getpgrp() {
		if syscall.Kill(-pgid, syscall.SIGKILL) == nil {
			return
		}
	}
	_ = cmd.Process.Kill()
}

func (s *session) waitForExit(timeout time.Duration) {
	s.mu.Lock()
	waitDone := s.waitDone
	s.mu.Unlock()
	if waitDone == nil {
		return
	}
	select {
	case <-waitDone:
	case <-time.After(timeout):
	}
}

func (s *session) resize(message windowChangeMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pty != nil {
		s.pty.Columns = message.Columns
		s.pty.Rows = message.Rows
		s.pty.Width = message.Width
		s.pty.Height = message.Height
	}
	if s.ptmx == nil {
		return
	}
	_ = pty.Setsize(s.ptmx, &pty.Winsize{
		Rows: uint16(message.Rows),
		Cols: uint16(message.Columns),
		X:    uint16(message.Width),
		Y:    uint16(message.Height),
	})
}

func (s *session) sendSignal(name string) {
	sig, ok := signalByName(name)
	if !ok {
		return
	}
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(sig)
	}
}

func (s *session) environmentLocked(ptyRequest *ptyRequestMsg) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, s.env...)
	if ptyRequest != nil && ptyRequest.Term != "" {
		env = append(env, "TERM="+ptyRequest.Term)
	}
	return mergeEnv(env)
}

func (s *session) workDir() string {
	if s.opts.WorkDir != "" {
		if info, err := os.Stat(s.opts.WorkDir); err == nil && info.IsDir() {
			return s.opts.WorkDir
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if info, err := os.Stat(home); err == nil && info.IsDir() {
			return home
		}
	}
	return ""
}

func mergeEnv(entries []string) []string {
	index := make(map[string]int, len(entries))
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			name = entry[:i]
		}
		if i, ok := index[name]; ok {
			result[i] = entry
			continue
		}
		index[name] = len(result)
		result = append(result, entry)
	}
	return result
}

func exitStatus(err error) uint32 {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return uint32(128 + int(status.Signal()))
			}
			if code := status.ExitStatus(); code >= 0 {
				return uint32(code)
			}
		}
		if code := exitErr.ExitCode(); code >= 0 {
			return uint32(code)
		}
	}
	return 255
}

var signalsByName = map[string]syscall.Signal{
	"ABRT":  syscall.SIGABRT,
	"ALRM":  syscall.SIGALRM,
	"CHLD":  syscall.SIGCHLD,
	"CONT":  syscall.SIGCONT,
	"HUP":   syscall.SIGHUP,
	"INT":   syscall.SIGINT,
	"KILL":  syscall.SIGKILL,
	"PIPE":  syscall.SIGPIPE,
	"QUIT":  syscall.SIGQUIT,
	"STOP":  syscall.SIGSTOP,
	"TERM":  syscall.SIGTERM,
	"TSTP":  syscall.SIGTSTP,
	"TTIN":  syscall.SIGTTIN,
	"TTOU":  syscall.SIGTTOU,
	"URG":   syscall.SIGURG,
	"USR1":  syscall.SIGUSR1,
	"USR2":  syscall.SIGUSR2,
	"WINCH": syscall.SIGWINCH,
}

func signalByName(name string) (syscall.Signal, bool) {
	sig, ok := signalsByName[strings.TrimPrefix(strings.ToUpper(name), "SIG")]
	return sig, ok
}
