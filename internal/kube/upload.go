package kube

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/jerer/ssh_k8s_proxy/internal/digest"
)

const writeScript = `set -e
dest="$1"
dir=$(dirname "$dest")
mkdir -p "$dir"
cat > "$dest"
chmod 0755 "$dest"
`

const renameScript = `set -e
mv -f "$1" "$2"
`

const removeScript = `rm -f "$1"
`

const uploadAttempts = 3

// Upload copies a local file into a container using a streaming exec session.
// The data is written to a temporary path, verified against the local SHA-256
// digest, and only then renamed over the destination. If a transfer is cut
// short, the existing destination file is left untouched.
func (c *Client) Upload(ctx context.Context, namespace, pod, container, source, destination string) error {
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open %s: %w", source, err)
	}
	defer file.Close()

	if strings.HasSuffix(destination, "/") {
		destination = path.Join(destination, path.Base(source))
	}

	expected, err := digest.File(source)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < uploadAttempts; attempt++ {
		suffix, err := randomSuffix()
		if err != nil {
			return err
		}
		temp := destination + ".upload-" + suffix

		if err := c.writeTemp(ctx, namespace, pod, container, file, temp); err != nil {
			lastErr = err
			c.removeTemp(ctx, namespace, pod, container, temp)
			if IsExitError(err) {
				return err
			}
			continue
		}

		ok, err := c.verifyTemp(ctx, namespace, pod, container, temp, expected)
		if err != nil {
			lastErr = err
			c.removeTemp(ctx, namespace, pod, container, temp)
			continue
		}
		if !ok {
			lastErr = fmt.Errorf("uploaded file digest does not match %s", source)
			c.removeTemp(ctx, namespace, pod, container, temp)
			continue
		}

		if err := c.renameTemp(ctx, namespace, pod, container, temp, destination); err != nil {
			lastErr = err
			c.removeTemp(ctx, namespace, pod, container, temp)
			continue
		}
		return nil
	}

	return fmt.Errorf("upload %s after %d attempts: %w", source, uploadAttempts, lastErr)
}

func (c *Client) writeTemp(ctx context.Context, namespace, pod, container string, file *os.File, temp string) error {
	var lastErr error
	for _, protocol := range c.protocols() {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind upload: %w", err)
		}

		stderr := &bytes.Buffer{}
		err := c.Exec(ctx, ExecOptions{
			Namespace: namespace,
			Pod:       pod,
			Container: container,
			Command:   []string{"sh", "-c", writeScript, "kubessh-upload", temp},
			Stdin:     file,
			Stdout:    io.Discard,
			Stderr:    stderr,
			Protocol:  protocol,
		})
		if err == nil {
			return nil
		}
		if stderr.Len() > 0 {
			lastErr = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		} else {
			lastErr = err
		}
		if IsExitError(err) {
			return lastErr
		}
	}
	return lastErr
}

func (c *Client) verifyTemp(ctx context.Context, namespace, pod, container, temp, expected string) (bool, error) {
	// Prefer sha256sum so verification also works for agent binaries that do
	// not have the "hash" subcommand. Fall back to the agent's own hash.
	out, sumErr := c.remoteOutput(ctx, namespace, pod, container, "sha256sum", temp)
	if sumErr == nil {
		fields := strings.Fields(out)
		if len(fields) > 0 {
			return strings.EqualFold(fields[0], expected), nil
		}
	}

	out, hashErr := c.remoteOutput(ctx, namespace, pod, container, temp, "hash")
	if hashErr == nil {
		return strings.EqualFold(strings.TrimSpace(out), expected), nil
	}
	return false, fmt.Errorf("verify uploaded file: sha256sum: %v; %s hash: %w", sumErr, temp, hashErr)
}

func (c *Client) renameTemp(ctx context.Context, namespace, pod, container, temp, destination string) error {
	return c.Exec(ctx, ExecOptions{
		Namespace: namespace,
		Pod:       pod,
		Container: container,
		Command:   []string{"sh", "-c", renameScript, "kubessh-rename", temp, destination},
		Stdout:    io.Discard,
		Stderr:    io.Discard,
		Protocol:  c.Protocol,
	})
}

func (c *Client) removeTemp(ctx context.Context, namespace, pod, container, temp string) {
	_ = c.Exec(ctx, ExecOptions{
		Namespace: namespace,
		Pod:       pod,
		Container: container,
		Command:   []string{"sh", "-c", removeScript, "kubessh-remove", temp},
		Stdout:    io.Discard,
		Stderr:    io.Discard,
		Protocol:  c.Protocol,
	})
}

// RemoteVersion returns the output of `<remote> version` inside the container.
func (c *Client) RemoteVersion(ctx context.Context, namespace, pod, container, remote string) (string, error) {
	return c.remoteOutput(ctx, namespace, pod, container, remote, "version")
}

// RemoteHash returns the SHA-256 digest printed by `<remote> hash`.
func (c *Client) RemoteHash(ctx context.Context, namespace, pod, container, remote string) (string, error) {
	return c.remoteOutput(ctx, namespace, pod, container, remote, "hash")
}

func (c *Client) remoteOutput(ctx context.Context, namespace, pod, container, remote string, args ...string) (string, error) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := c.Exec(ctx, ExecOptions{
		Namespace: namespace,
		Pod:       pod,
		Container: container,
		Command:   append([]string{remote}, args...),
		Stdout:    stdout,
		Stderr:    stderr,
	})
	if err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func randomSuffix() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate upload suffix: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
