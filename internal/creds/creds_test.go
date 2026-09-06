package creds

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Rake-Pro/mailshear/internal/config"
)

func TestResolveEnv(t *testing.T) {
	t.Setenv("UNSUB_TEST_PW", "s3cret")
	a := config.Account{Name: "acct", Username: "user", Auth: "env:UNSUB_TEST_PW"}
	pw, err := Resolve(t.TempDir(), a)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pw != "s3cret" {
		t.Errorf("got %q, want s3cret", pw)
	}
}

func TestResolveEnvEmpty(t *testing.T) {
	t.Setenv("UNSUB_TEST_PW_EMPTY", "")
	a := config.Account{Name: "acct", Username: "user", Auth: "env:UNSUB_TEST_PW_EMPTY"}
	if _, err := Resolve(t.TempDir(), a); err == nil {
		t.Fatal("expected error for empty env var")
	}
}

func TestResolveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(path, []byte("filepw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := config.Account{Name: "acct", Username: "user", Auth: "file:" + path}
	pw, err := Resolve(t.TempDir(), a)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pw != "filepw" {
		t.Errorf("got %q, want filepw", pw)
	}
}

func TestResolveFileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := config.Account{Name: "acct", Username: "user", Auth: "file:" + path}
	if _, err := Resolve(t.TempDir(), a); err == nil {
		t.Fatal("expected error for empty file")
	}
}

func TestResolveFileMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")
	a := config.Account{Name: "acct", Username: "user", Auth: "file:" + path}
	if _, err := Resolve(t.TempDir(), a); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestResolveFileHomeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "pw.txt"), []byte("homepw"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := config.Account{Name: "acct", Username: "user", Auth: "file:~/pw.txt"}
	pw, err := Resolve(t.TempDir(), a)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pw != "homepw" {
		t.Errorf("got %q, want homepw", pw)
	}
}

func TestResolveInvalidAuth(t *testing.T) {
	a := config.Account{Name: "acct", Username: "user", Auth: "bogus"}
	if _, err := Resolve(t.TempDir(), a); err == nil {
		t.Fatal("expected error for invalid auth spec")
	}
}

func TestSetResolveDeleteRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	a := config.Account{Name: "acct", Username: "user", Auth: "file"}

	if err := Set(dataDir, a, "s3cret"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	pw, err := Resolve(dataDir, a)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pw != "s3cret" {
		t.Errorf("got %q, want s3cret", pw)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(credentialPath(dataDir, a))
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("credential file mode = %o, want 0600", mode)
		}
	}

	if err := Delete(dataDir, a); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Resolve(dataDir, a); err == nil {
		t.Fatal("expected error resolving deleted credential")
	}
	if err := Delete(dataDir, a); err == nil {
		t.Fatal("expected error deleting an already-deleted credential")
	}
}

func TestKeyringAuthAliasResolvesFileStore(t *testing.T) {
	dataDir := t.TempDir()
	a := config.Account{Name: "acct", Username: "user", Auth: "keyring"}

	if err := Set(dataDir, a, "aliaspw"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	pw, err := Resolve(dataDir, a)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pw != "aliaspw" {
		t.Errorf("got %q, want aliaspw", pw)
	}
}

func TestNormalizeGmailAppPassword(t *testing.T) {
	g := config.Account{Name: "g", Host: "imap.gmail.com"}
	if got := Normalize(g, "abcd efgh ijkl mnop"); got != "abcdefghijklmnop" {
		t.Fatalf("gmail: got %q", got)
	}
	o := config.Account{Name: "o", Host: "imap.example.com"}
	if got := Normalize(o, "pass word"); got != "pass word" {
		t.Fatalf("generic: got %q", got)
	}
	t.Setenv("UNSUB_T_PW", "abcd efgh ijkl mnop")
	g.Auth = "env:UNSUB_T_PW"
	pw, err := Resolve(t.TempDir(), g)
	if err != nil || pw != "abcdefghijklmnop" {
		t.Fatalf("resolve: %q %v", pw, err)
	}
}
