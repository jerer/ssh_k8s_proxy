package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newTestKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	key, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}
	return key, string(ssh.MarshalAuthorizedKey(key))
}

func TestBuildAuthInlineKey(t *testing.T) {
	t.Setenv("KUBESSH_AUTHORIZED_KEYS", "")
	t.Setenv("KUBESSH_AUTHORIZED_KEYS_FILES", "")
	t.Setenv("KUBESSH_PASSWORD", "")

	key, line := newTestKey(t)
	empty := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write empty authorized keys: %v", err)
	}
	auth, err := buildAuth(AuthOptions{
		AuthorizedKeys:      []string{line},
		AuthorizedKeysFiles: []string{empty},
	})
	if err != nil {
		t.Fatalf("build auth: %v", err)
	}
	if len(auth.keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(auth.keys))
	}
	if !keyAllowed(auth.keys, key) {
		t.Fatal("authorized key was rejected")
	}

	other, _ := newTestKey(t)
	if keyAllowed(auth.keys, other) {
		t.Fatal("unauthorized key was accepted")
	}
}

func TestBuildAuthFile(t *testing.T) {
	t.Setenv("KUBESSH_AUTHORIZED_KEYS", "")
	t.Setenv("KUBESSH_AUTHORIZED_KEYS_FILES", "")
	t.Setenv("KUBESSH_PASSWORD", "")

	key, line := newTestKey(t)
	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, []byte("# comment\n\n"+line+"\n"), 0o600); err != nil {
		t.Fatalf("write authorized keys: %v", err)
	}

	auth, err := buildAuth(AuthOptions{AuthorizedKeysFiles: []string{path}})
	if err != nil {
		t.Fatalf("build auth: %v", err)
	}
	if !keyAllowed(auth.keys, key) {
		t.Fatal("key from file was rejected")
	}
}

func TestPasswordMatches(t *testing.T) {
	if !passwordMatches("secret", "secret") {
		t.Fatal("matching passwords rejected")
	}
	if passwordMatches("secret", "Secret") {
		t.Fatal("different password accepted")
	}
	if passwordMatches("short", "a much longer password") {
		t.Fatal("different length password accepted")
	}
}

func TestBuildAuthExplicitFileMissing(t *testing.T) {
	t.Setenv("KUBESSH_AUTHORIZED_KEYS", "")
	t.Setenv("KUBESSH_AUTHORIZED_KEYS_FILES", "")
	t.Setenv("KUBESSH_PASSWORD", "")

	_, err := buildAuth(AuthOptions{
		AuthorizedKeysFiles: []string{filepath.Join(t.TempDir(), "missing")},
	})
	if err == nil {
		t.Fatal("expected an error for an explicitly configured missing key file")
	}
}
