package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

// DefaultHostKeyPath is the persistent host key location used by the agent.
// Inside a container /tmp usually survives for the lifetime of the pod, which
// keeps the SSH host key stable across ProxyCommand invocations.
const DefaultHostKeyPath = "/tmp/kubessh/host_ed25519"

func loadOrCreateHostKey(path string, warnf func(string, ...any)) (ssh.Signer, error) {
	if path == "" {
		if warnf != nil {
			warnf("no host key path configured, generating an ephemeral host key")
		}
		return generateEphemeralHostKey()
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		if warnf != nil {
			warnf("cannot create host key directory for %s: %v (using ephemeral host key)", path, err)
		}
		return generateEphemeralHostKey()
	}

	signer, err := readHostKey(path)
	if err == nil {
		return signer, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	signer, raw, err := generateHostKey()
	if err != nil {
		return nil, err
	}

	// Write the key to a temporary file and hard-link it into place. The link
	// is atomic and fails if another agent won the race, so concurrent first
	// connections always share one host key.
	temp, err := os.CreateTemp(dir, ".host_ed25519-*")
	if err != nil {
		if warnf != nil {
			warnf("cannot create temporary host key in %s: %v (using ephemeral host key)", dir, err)
		}
		return signer, nil
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	written := false
	if _, err := temp.Write(raw); err == nil {
		if err := temp.Sync(); err == nil {
			written = true
		}
	}
	if err := temp.Close(); err != nil {
		written = false
	}
	if !written {
		if warnf != nil {
			warnf("cannot write host key %s (using ephemeral host key)", path)
		}
		return signer, nil
	}

	if err := os.Link(tempName, path); err == nil {
		return signer, nil
	} else if errors.Is(err, fs.ErrExist) {
		return readHostKeyWithRetry(path)
	}

	// Some filesystems do not support hard links. Fall back to an exclusive
	// create so we still never overwrite a key created by a concurrent agent.
	if err := writeHostKeyExclusive(path, raw); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return readHostKeyWithRetry(path)
		}
		if warnf != nil {
			warnf("cannot write host key %s: %v (using ephemeral host key)", path, err)
		}
		return signer, nil
	}
	return signer, nil
}

func readHostKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse host key %s: %w", path, err)
	}
	return signer, nil
}

func readHostKeyWithRetry(path string) (ssh.Signer, error) {
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		signer, err := readHostKey(path)
		if err == nil {
			return signer, nil
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	return nil, lastErr
}

func writeHostKeyExclusive(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	return file.Close()
}

func generateEphemeralHostKey() (ssh.Signer, error) {
	signer, _, err := generateHostKey()
	return signer, err
}

func generateHostKey() (ssh.Signer, []byte, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ed25519 host key: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(privateKey, "kubessh")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal host key: %w", err)
	}

	raw := pem.EncodeToMemory(block)
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create host key signer: %w", err)
	}
	return signer, raw, nil
}
