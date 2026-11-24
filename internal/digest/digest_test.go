package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(path, []byte("kubessh"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	got, err := File(path)
	if err != nil {
		t.Fatalf("hash file: %v", err)
	}

	sum := sha256.Sum256([]byte("kubessh"))
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
}

func TestFileMissing(t *testing.T) {
	if _, err := File(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestSelf(t *testing.T) {
	got, err := Self()
	if err != nil {
		t.Fatalf("hash self: %v", err)
	}
	if len(got) != 64 {
		t.Fatalf("digest length = %d, want 64", len(got))
	}
}
