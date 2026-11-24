package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sync/atomic"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
	"k8s.io/klog/v2"
)

// Protocol selects the exec transport. Auto prefers WebSocket and falls back
// to SPDY when the WebSocket upgrade does not succeed.
type Protocol string

const (
	ProtocolAuto      Protocol = "auto"
	ProtocolWebSocket Protocol = "websocket"
	ProtocolSPDY      Protocol = "spdy"
)

// ExecOptions describes a pods/exec invocation.
type ExecOptions struct {
	Namespace string
	Pod       string
	Container string
	Command   []string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	Protocol  Protocol
}

// Exec runs a command inside a container and streams stdin/stdout/stderr.
func (c *Client) Exec(ctx context.Context, opts ExecOptions) error {
	namespace := opts.Namespace
	if namespace == "" {
		namespace = c.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}

	execOptions := &corev1.PodExecOptions{
		Container: opts.Container,
		Command:   opts.Command,
		Stdin:     opts.Stdin != nil,
		Stdout:    opts.Stdout != nil,
		Stderr:    opts.Stderr != nil,
		TTY:       false,
	}

	request := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(opts.Pod).
		SubResource("exec").
		VersionedParams(execOptions, kubescheme.ParameterCodec)

	protocol := opts.Protocol
	if protocol == "" {
		protocol = c.Protocol
	}

	var transferred atomic.Bool
	stdin := wrapReader(opts.Stdin, &transferred)
	stdout := wrapWriter(opts.Stdout, &transferred)

	streamOptions := remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: opts.Stderr,
	}

	executor, err := c.executor(request.URL(), protocol, func() bool {
		return !transferred.Load()
	})
	if err != nil {
		return err
	}
	// client-go logs harmless "use of closed network connection" errors when an
	// interactive stream is closed. They are already reflected in the returned
	// error, so keep them off the user's stderr.
	ctx = klog.NewContext(ctx, logr.Discard())
	return executor.StreamWithContext(ctx, streamOptions)
}

func (c *Client) executor(u *url.URL, protocol Protocol, fallbackAllowed func() bool) (remotecommand.Executor, error) {
	switch protocol {
	case ProtocolSPDY:
		return remotecommand.NewSPDYExecutor(c.Config, "POST", u)
	case ProtocolWebSocket:
		return remotecommand.NewWebSocketExecutor(c.Config, "GET", u.String())
	case "", ProtocolAuto:
		websocket, wsErr := remotecommand.NewWebSocketExecutor(c.Config, "GET", u.String())
		spdy, spdyErr := remotecommand.NewSPDYExecutor(c.Config, "POST", u)
		switch {
		case wsErr == nil && spdyErr == nil:
			return remotecommand.NewFallbackExecutor(websocket, spdy, func(err error) bool {
				return shouldFallback(err) && (fallbackAllowed == nil || fallbackAllowed())
			})
		case wsErr == nil:
			return websocket, nil
		case spdyErr == nil:
			return spdy, nil
		default:
			return nil, fmt.Errorf("create exec transport: websocket: %v; spdy: %w", wsErr, spdyErr)
		}
	default:
		return nil, fmt.Errorf("unknown exec protocol %q (want auto, websocket or spdy)", protocol)
	}
}

func shouldFallback(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return !IsExitError(err)
}

// IsExitError reports whether the remote command ran and returned a non-zero
// exit status. Such errors must not be retried on another transport.
func IsExitError(err error) bool {
	var exitErr k8sexec.ExitError
	return errors.As(err, &exitErr)
}

type trackingReader struct {
	reader io.Reader
	flag   *atomic.Bool
}

func wrapReader(reader io.Reader, flag *atomic.Bool) io.Reader {
	if reader == nil {
		return nil
	}
	return &trackingReader{reader: reader, flag: flag}
}

func (r *trackingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.flag.Store(true)
	}
	return n, err
}

type trackingWriter struct {
	writer io.Writer
	flag   *atomic.Bool
}

func wrapWriter(writer io.Writer, flag *atomic.Bool) io.Writer {
	if writer == nil {
		return nil
	}
	return &trackingWriter{writer: writer, flag: flag}
}

func (w *trackingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 {
		w.flag.Store(true)
	}
	return n, err
}
