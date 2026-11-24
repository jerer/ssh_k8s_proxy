// Package digest computes SHA-256 digests for files and the running binary.
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// File returns the hex-encoded SHA-256 digest of the file at path.
func File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	sum, err := Reader(file)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return sum, nil
}

// Reader returns the hex-encoded SHA-256 digest of r.
func Reader(r io.Reader) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Self returns the hex-encoded SHA-256 digest of the running executable.
func Self() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find executable: %w", err)
	}
	return File(path)
}
