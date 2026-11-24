package kube

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// DialPortForward opens a single TCP connection to a port inside a pod using
// the Kubernetes port-forward subresource. The returned cleanup function must
// be called once the connection is no longer needed.
func (c *Client) DialPortForward(ctx context.Context, namespace, pod string, remotePort int) (net.Conn, func(), error) {
	if namespace == "" {
		namespace = c.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}
	if remotePort < 1 || remotePort > 65535 {
		return nil, nil, fmt.Errorf("invalid remote port %d", remotePort)
	}

	roundTripper, upgrader, err := spdy.RoundTripperFor(c.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("create port-forward transport: %w", err)
	}

	request := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("portforward")

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, request.URL())

	stop := make(chan struct{})
	ready := make(chan struct{})
	errOut := &bytes.Buffer{}
	var stopOnce sync.Once
	stopForwarding := func() { stopOnce.Do(func() { close(stop) }) }

	pf, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", remotePort)}, stop, ready, io.Discard, errOut)
	if err != nil {
		return nil, nil, fmt.Errorf("create port-forward: %w", err)
	}

	forwardErr := make(chan error, 1)
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		forwardErr <- pf.ForwardPorts()
	}()

	go func() {
		select {
		case <-ctx.Done():
			stopForwarding()
		case <-forwardDone:
		}
	}()

	select {
	case <-ctx.Done():
		stopForwarding()
		return nil, nil, ctx.Err()
	case err := <-forwardErr:
		stopForwarding()
		return nil, nil, formatPortForwardError(err, errOut)
	case <-ready:
	}

	ports, err := pf.GetPorts()
	if err != nil {
		stopForwarding()
		return nil, nil, fmt.Errorf("get forwarded ports: %w", err)
	}
	if len(ports) == 0 {
		stopForwarding()
		return nil, nil, fmt.Errorf("port-forward returned no ports")
	}

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", ports[0].Local)))
	if err != nil {
		stopForwarding()
		return nil, nil, fmt.Errorf("dial local port-forward: %w", err)
	}

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			_ = conn.Close()
			stopForwarding()
		})
	}
	return conn, cleanup, nil
}

func formatPortForwardError(err error, stderr *bytes.Buffer) error {
	if err == nil {
		err = fmt.Errorf("port-forward stopped")
	}
	if stderr != nil && stderr.Len() > 0 {
		return fmt.Errorf("port-forward: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return fmt.Errorf("port-forward: %w", err)
}
