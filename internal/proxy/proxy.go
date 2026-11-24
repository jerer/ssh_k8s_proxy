package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jerer/ssh_k8s_proxy/internal/digest"
	"github.com/jerer/ssh_k8s_proxy/internal/embedded"
	"github.com/jerer/ssh_k8s_proxy/internal/kube"
)

// Options configures a ProxyCommand connection to a container.
type Options struct {
	Namespace       string
	Pod             string
	Container       string
	Remote          string
	AgentBinary     string
	UploadMode      string
	Via             string
	RemotePort      int
	Protocol        kube.Protocol
	HostKey         string
	Shell           string
	Version         string
	AgentArch       string
	DownloadURL     string
	DownloadTimeout time.Duration
}

// Run connects the local SSH client (in/out) to the SSH agent inside the pod.
func Run(ctx context.Context, client *kube.Client, opts Options, in io.Reader, out io.Writer, errOut io.Writer) error {
	if opts.Pod == "" {
		return fmt.Errorf("pod is required")
	}
	if opts.Via == "" {
		opts.Via = "exec"
	}
	if opts.UploadMode == "" {
		opts.UploadMode = "auto"
	}

	switch opts.Via {
	case "exec":
		return runExec(ctx, client, opts, in, out, errOut)
	case "portforward", "port-forward":
		return runPortForward(ctx, client, opts, in, out)
	default:
		return fmt.Errorf("unknown transport %q (want exec or portforward)", opts.Via)
	}
}

func runExec(ctx context.Context, client *kube.Client, opts Options, in io.Reader, out io.Writer, errOut io.Writer) error {
	if opts.Remote == "" {
		return fmt.Errorf("remote agent path is required")
	}

	if err := ensureAgent(ctx, client, opts, errOut); err != nil {
		return err
	}

	command := []string{opts.Remote, "agent", "--stdio"}
	if opts.HostKey != "" {
		command = append(command, "--host-key", opts.HostKey)
	}
	if opts.Shell != "" {
		command = append(command, "--shell", opts.Shell)
	}

	return client.Exec(ctx, kube.ExecOptions{
		Namespace: opts.Namespace,
		Pod:       opts.Pod,
		Container: opts.Container,
		Command:   command,
		Stdin:     in,
		Stdout:    out,
		Stderr:    errOut,
		Protocol:  opts.Protocol,
	})
}

func ensureAgent(ctx context.Context, client *kube.Client, opts Options, errOut io.Writer) error {
	switch opts.UploadMode {
	case "never":
		return nil
	case "auto", "always", "download":
	default:
		return fmt.Errorf("unknown upload mode %q (want auto, always, download or never)", opts.UploadMode)
	}

	source, err := resolveLocalAgent(opts.AgentBinary)
	if err != nil {
		return err
	}

	switch opts.UploadMode {
	case "always":
		if source == nil {
			return fmt.Errorf("--upload-mode=always requires --agent-binary or an embedded agent")
		}
		return uploadSource(ctx, client, opts, source, errOut)

	case "download":
		matched, err := remoteMatches(ctx, client, opts, source)
		if err != nil {
			return err
		}
		if matched {
			return nil
		}
		if err := downloadAgent(ctx, client, opts, source, errOut); err != nil {
			return withDownloadHint(err)
		}
		return nil

	case "auto":
		matched, err := remoteMatches(ctx, client, opts, source)
		if err != nil {
			return err
		}
		if matched {
			return nil
		}

		downloadErr := downloadAgent(ctx, client, opts, source, errOut)
		if downloadErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return downloadErr
		}
		if source == nil {
			return withDownloadHint(downloadErr)
		}

		if errOut != nil {
			fmt.Fprintf(errOut, "kubessh: download failed: %v; falling back to upload\n", downloadErr)
		}
		return uploadSource(ctx, client, opts, source, errOut)
	}
	return nil
}

func remoteMatches(ctx context.Context, client *kube.Client, opts Options, source *localAgent) (bool, error) {
	if source != nil {
		localHash, err := source.hash()
		if err != nil {
			return false, fmt.Errorf("hash agent binary: %w", err)
		}
		remoteHash, err := client.RemoteHash(ctx, opts.Namespace, opts.Pod, opts.Container, opts.Remote)
		if err != nil {
			return false, nil
		}
		return strings.EqualFold(strings.TrimSpace(remoteHash), localHash), nil
	}

	remoteVersion, err := client.RemoteVersion(ctx, opts.Namespace, opts.Pod, opts.Container, opts.Remote)
	if err != nil {
		return false, nil
	}
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "dev"
	}
	return strings.TrimSpace(remoteVersion) == version, nil
}

const defaultGitHubRepo = "jerer/ssh_k8s_proxy"

func agentDownloadURL(opts Options) string {
	arch := strings.TrimSpace(opts.AgentArch)
	if arch == "" {
		arch = "amd64"
	}
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "dev"
	}

	if opts.DownloadURL != "" {
		return strings.NewReplacer(
			"{version}", version,
			"{arch}", arch,
			"{os}", "linux",
		).Replace(opts.DownloadURL)
	}

	if version == "dev" {
		return fmt.Sprintf("https://github.com/%s/releases/latest/download/kubessh-linux-%s", defaultGitHubRepo, arch)
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/kubessh-linux-%s", defaultGitHubRepo, url.PathEscape(version), arch)
}

func downloadAgent(ctx context.Context, client *kube.Client, opts Options, source *localAgent, errOut io.Writer) error {
	expected := ""
	if source != nil {
		var err error
		expected, err = source.hash()
		if err != nil {
			return fmt.Errorf("hash agent binary: %w", err)
		}
	}

	timeout := opts.DownloadTimeout
	if timeout <= 0 {
		timeout = time.Minute
	}

	url := agentDownloadURL(opts)
	if errOut != nil {
		fmt.Fprintf(errOut, "kubessh: downloading agent from %s\n", url)
	}

	downloadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := client.DownloadAgent(downloadCtx, opts.Namespace, opts.Pod, opts.Container, opts.Remote, url, expected, timeout); err != nil {
		return fmt.Errorf("download agent: %w", err)
	}
	return nil
}

func withDownloadHint(err error) error {
	return fmt.Errorf("%w (install curl or wget in the pod, use --agent-binary, or run kubessh inject)", err)
}

type localAgent struct {
	path string
	gzip []byte
}

func resolveLocalAgent(binary string) (*localAgent, error) {
	if binary != "" {
		return &localAgent{path: binary}, nil
	}
	data, ok := embedded.LinuxAMD64()
	if !ok {
		return nil, nil
	}
	return &localAgent{gzip: data}, nil
}

func (a *localAgent) hash() (string, error) {
	if a.path != "" {
		return digest.File(a.path)
	}
	reader, err := gzip.NewReader(bytes.NewReader(a.gzip))
	if err != nil {
		return "", fmt.Errorf("open embedded agent: %w", err)
	}
	defer reader.Close()
	sum, err := digest.Reader(reader)
	if err != nil {
		return "", fmt.Errorf("hash embedded agent: %w", err)
	}
	return sum, nil
}

func (a *localAgent) materialize() (string, func(), error) {
	if a.path != "" {
		return a.path, func() {}, nil
	}

	file, err := os.CreateTemp("", "kubessh-agent-linux-amd64-*")
	if err != nil {
		return "", nil, fmt.Errorf("extract embedded agent: %w", err)
	}
	name := file.Name()
	cleanup := func() { _ = os.Remove(name) }

	reader, err := gzip.NewReader(bytes.NewReader(a.gzip))
	if err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("open embedded agent: %w", err)
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := reader.Close()
	fileErr := file.Close()
	if copyErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("extract embedded agent: %w", copyErr)
	}
	if closeErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("extract embedded agent: %w", closeErr)
	}
	if fileErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("extract embedded agent: %w", fileErr)
	}
	return name, cleanup, nil
}

func uploadSource(ctx context.Context, client *kube.Client, opts Options, source *localAgent, errOut io.Writer) error {
	path, cleanup, err := source.materialize()
	if err != nil {
		return err
	}
	defer cleanup()

	if errOut != nil {
		fmt.Fprintf(errOut, "kubessh: uploading agent to %s\n", opts.Remote)
	}
	if err := client.Upload(ctx, opts.Namespace, opts.Pod, opts.Container, path, opts.Remote); err != nil {
		return fmt.Errorf("upload agent: %w", err)
	}
	return nil
}

func runPortForward(ctx context.Context, client *kube.Client, opts Options, in io.Reader, out io.Writer) error {
	port := opts.RemotePort
	if port == 0 {
		port = 2222
	}
	conn, cleanup, err := client.DialPortForward(ctx, opts.Namespace, opts.Pod, port)
	if err != nil {
		return err
	}
	defer cleanup()
	return bridge(ctx, conn, in, out)
}

func bridge(ctx context.Context, conn net.Conn, in io.Reader, out io.Writer) error {
	done := make(chan error, 2)

	go func() {
		_, err := io.Copy(conn, in)
		if closer, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		} else {
			_ = conn.Close()
		}
		done <- err
	}()

	go func() {
		_, err := io.Copy(out, conn)
		done <- err
	}()

	cancelDone := make(chan struct{})
	defer close(cancelDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-cancelDone:
		}
	}()

	err := <-done
	_ = conn.Close()

	if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("bridge connection: %w", err)
	}
	return ctx.Err()
}
