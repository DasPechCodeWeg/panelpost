package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripAndKeyFile(t *testing.T) {
	dir := t.TempDir()
	box, err := LoadOrCreate("", dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v", info.Mode().Perm())
	}
	sealed, err := box.Seal("glsa_supersecret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "supersecret") {
		t.Fatal("plaintext visible in sealed value")
	}
	again, _ := LoadOrCreate("", dir)
	plain, err := again.Open(sealed)
	if err != nil || plain != "glsa_supersecret" {
		t.Fatalf("open = %q, %v", plain, err)
	}
	other, _ := New([]byte("different"))
	if _, err := other.Open(sealed); err == nil {
		t.Error("wrong key opened the secret")
	}
	if s, _ := box.Seal(""); s != "" {
		t.Error("empty secret should stay empty")
	}
	if _, err := box.Open("plain"); err == nil {
		t.Error("unencrypted value accepted")
	}
}
