package agent

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// AuthOptions configures SSH authentication for the agent.
//
// If no authorized keys and no password are configured the server accepts
// "none" authentication. This is the common ProxyCommand case: Kubernetes
// RBAC (pods/exec) is the real access boundary. When listening on a network
// address you should configure keys or a password.
type AuthOptions struct {
	AuthorizedKeys      []string
	AuthorizedKeysFiles []string
	Password            string
	AllowNone           bool
}

type authMethods struct {
	keys     []ssh.PublicKey
	password string
}

func buildAuth(opts AuthOptions) (*authMethods, error) {
	keys, err := loadAuthorizedKeys(opts)
	if err != nil {
		return nil, err
	}

	password := opts.Password
	if password == "" {
		password = os.Getenv("KUBESSH_PASSWORD")
	}

	return &authMethods{keys: keys, password: password}, nil
}

func loadAuthorizedKeys(opts AuthOptions) ([]ssh.PublicKey, error) {
	var keys []ssh.PublicKey

	for _, inline := range opts.AuthorizedKeys {
		keys = append(keys, parseInlineKeys(inline)...)
	}
	keys = append(keys, parseInlineKeys(os.Getenv("KUBESSH_AUTHORIZED_KEYS"))...)

	files := opts.AuthorizedKeysFiles
	explicitFiles := len(files) > 0
	if !explicitFiles {
		if env := os.Getenv("KUBESSH_AUTHORIZED_KEYS_FILES"); env != "" {
			files = splitList(env)
			explicitFiles = len(files) > 0
		}
	}
	if len(files) == 0 {
		files = defaultAuthorizedKeyFiles()
	}

	for _, name := range files {
		fileKeys, err := readAuthorizedKeysFile(name, !explicitFiles)
		if err != nil {
			return nil, err
		}
		keys = append(keys, fileKeys...)
	}

	return dedupeKeys(keys), nil
}

func parseInlineKeys(value string) []ssh.PublicKey {
	var keys []ssh.PublicKey
	for _, line := range strings.Split(value, "\n") {
		rest := []byte(strings.TrimSpace(line))
		if len(rest) == 0 || strings.HasPrefix(string(rest), "#") {
			continue
		}
		for len(rest) > 0 && !strings.HasPrefix(string(rest), "#") {
			key, _, _, next, err := ssh.ParseAuthorizedKey(rest)
			if err != nil {
				break
			}
			keys = append(keys, key)
			rest = bytes.TrimSpace(next)
		}
	}
	return keys
}

func readAuthorizedKeysFile(name string, ignoreMissing bool) ([]ssh.PublicKey, error) {
	data, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		if ignoreMissing && os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read authorized keys %s: %w", name, err)
	}

	var keys []ssh.PublicKey
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("parse authorized keys %s:%d: %w", name, i+1, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func defaultAuthorizedKeyFiles() []string {
	var files []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		files = append(files, filepath.Join(home, ".ssh", "authorized_keys"))
	}
	files = append(files, "/etc/kubessh/authorized_keys")
	if home, err := os.UserHomeDir(); err != nil || home != "/root" {
		files = append(files, "/root/.ssh/authorized_keys")
	}
	return dedupeStrings(files)
}

func dedupeKeys(keys []ssh.PublicKey) []ssh.PublicKey {
	seen := make(map[string]struct{}, len(keys))
	result := keys[:0]
	for _, key := range keys {
		id := string(key.Marshal())
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, key)
	}
	return result
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := values[:0]
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func splitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == ':'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			out = append(out, field)
		}
	}
	return out
}

func keyAllowed(allowed []ssh.PublicKey, presented ssh.PublicKey) bool {
	marshaled := presented.Marshal()
	for _, key := range allowed {
		if subtle.ConstantTimeCompare(marshaled, key.Marshal()) == 1 {
			return true
		}
	}
	return false
}

func passwordMatches(expected, presented string) bool {
	expectedHash := sha256.Sum256([]byte(expected))
	presentedHash := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(expectedHash[:], presentedHash[:]) == 1
}
