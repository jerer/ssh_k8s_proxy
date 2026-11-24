package kube

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const downloadScript = `set -e
dest="$1"
url="$2"
expected="$3"
timeout="$4"
dir=$(dirname "$dest")
mkdir -p "$dir"
tmp="$dest.download-$$"
trap 'rm -f "$tmp"' EXIT
if command -v curl >/dev/null 2>&1; then
  curl -fsSL --connect-timeout 15 --max-time "$timeout" -o "$tmp" "$url"
elif command -v wget >/dev/null 2>&1; then
  wget -q -T "$timeout" -O "$tmp" "$url"
else
  echo "neither curl nor wget is available in the container" >&2
  exit 127
fi
chmod 0755 "$tmp"
if [ -n "$expected" ]; then
  actual=""
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$tmp")
    actual=${actual%% *}
  elif [ -x "$dest" ]; then
    actual=$("$dest" hash 2>/dev/null || true)
  fi
  if [ -z "$actual" ]; then
    echo "cannot verify downloaded agent (need sha256sum or an existing agent)" >&2
    exit 1
  fi
  if [ "$actual" != "$expected" ]; then
    echo "downloaded agent checksum mismatch" >&2
    exit 1
  fi
fi
mv -f "$tmp" "$dest"
`

// DownloadAgent downloads the agent from url inside the container, optionally
// verifies its SHA-256 digest, and atomically installs it at destination.
func (c *Client) DownloadAgent(ctx context.Context, namespace, pod, container, destination, url, expected string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = time.Minute
	}

	stderr := &bytes.Buffer{}
	err := c.Exec(ctx, ExecOptions{
		Namespace: namespace,
		Pod:       pod,
		Container: container,
		Command: []string{
			"sh", "-c", downloadScript, "kubessh-download",
			destination, url, expected, strconv.Itoa(int(timeout.Seconds())),
		},
		Stdout: io.Discard,
		Stderr: stderr,
	})
	if err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return err
	}
	return nil
}
