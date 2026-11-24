//go:build !windows

package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadOrCreateHostKeyPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "hostkey")

	first, err := loadOrCreateHostKey(path, nil)
	if err != nil {
		t.Fatalf("create host key: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat host key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("host key mode = %o, want 600", info.Mode().Perm())
	}

	second, err := loadOrCreateHostKey(path, nil)
	if err != nil {
		t.Fatalf("load host key: %v", err)
	}

	if !bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal()) {
		t.Fatal("host key changed between loads")
	}
}

func TestLoadOrCreateHostKeyEphemeral(t *testing.T) {
	first, err := loadOrCreateHostKey("", nil)
	if err != nil {
		t.Fatalf("create ephemeral host key: %v", err)
	}
	second, err := loadOrCreateHostKey("", nil)
	if err != nil {
		t.Fatalf("create second ephemeral host key: %v", err)
	}
	if bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal()) {
		t.Fatal("ephemeral host keys should not be deterministic")
	}
}

func TestLoadOrCreateHostKeyConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hostkey")

	const workers = 20
	start := make(chan struct{})
	results := make([][]byte, workers)
	errs := make([]error, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			signer, err := loadOrCreateHostKey(path, nil)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = signer.PublicKey().Marshal()
		}(i)
	}

	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	for i := 1; i < workers; i++ {
		if !bytes.Equal(results[0], results[i]) {
			t.Fatalf("worker %d got a different host key", i)
		}
	}
}
