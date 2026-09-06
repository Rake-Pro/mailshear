// Package creds resolves account passwords per the account's auth spec
// (a local credential file, an environment variable, or an explicit file)
// and manages that credential file store.
package creds

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rake-Pro/mailshear/internal/config"
)

func Resolve(dataDir string, a config.Account) (string, error) {
	var pw string
	var err error
	switch {
	case a.Auth == "file" || a.Auth == "keyring":
		pw, err = resolveStore(dataDir, a)
	case strings.HasPrefix(a.Auth, "env:"):
		pw, err = resolveEnv(a, strings.TrimPrefix(a.Auth, "env:"))
	case strings.HasPrefix(a.Auth, "file:"):
		pw, err = resolveFile(a, strings.TrimPrefix(a.Auth, "file:"))
	case a.Auth == "oauth":
		return "", fmt.Errorf("account %s signs in with OAuth and has no password; use creds.AccessToken", a.Name)
	default:
		return "", fmt.Errorf("account %s: invalid auth spec %q", a.Name, a.Auth)
	}
	if err != nil {
		return "", err
	}
	return Normalize(a, pw), nil
}

// Normalize strips the display spaces Google puts in app passwords
// ("xxxx xxxx xxxx xxxx"); other providers keep the password as typed.
func Normalize(a config.Account, pw string) string {
	if a.Provider() == "gmail" {
		return strings.Join(strings.Fields(pw), "")
	}
	return pw
}

func credentialPath(dataDir string, a config.Account) string {
	return filepath.Join(dataDir, "credentials", a.Name)
}

func resolveStore(dataDir string, a config.Account) (string, error) {
	path := credentialPath(dataDir, a)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no password stored for account %s; add it on the accounts screen", a.Name)
		}
		return "", fmt.Errorf("account %s: reading credential file %s: %w", a.Name, path, err)
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", fmt.Errorf("account %s: credential file %s is empty; re-enter the password on the accounts screen", a.Name, path)
	}
	return v, nil
}

func resolveEnv(a config.Account, name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("environment variable %s is empty; set it or change account %s's auth spec", name, a.Name)
	}
	return v, nil
}

func resolveFile(a config.Account, path string) (string, error) {
	path = expandHome(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("account %s: reading password file %s: %w", a.Name, path, err)
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", fmt.Errorf("account %s: password file %s is empty", a.Name, path)
	}
	return v, nil
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// Set stores password in <dataDir>/credentials/<account name>, creating the
// credentials directory (mode 0700) if needed and writing the file
// atomically with mode 0600.
func Set(dataDir string, a config.Account, password string) error {
	return writeSecret(dataDir, a.Name, []byte(password), credentialPath(dataDir, a))
}

// writeSecret writes data to dest through a temporary file in the same
// directory, so a reader never sees a half-written credential. The
// directory is created at mode 0700 and the file at 0600. prefix only names
// the temporary file.
func writeSecret(dataDir, prefix string, data []byte, dest string) error {
	dir := filepath.Join(dataDir, "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating credentials directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, prefix+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temporary credential file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing credential file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing credential file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("setting credential file permissions: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("storing credential file: %w", err)
	}
	return nil
}

// ErrNoPassword reports that the account has no stored password to delete.
var ErrNoPassword = errors.New("no password stored")

func Delete(dataDir string, a config.Account) error {
	path := credentialPath(dataDir, a)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("account %s: %w", a.Name, ErrNoPassword)
		}
		return fmt.Errorf("account %s: deleting credential file %s: %w", a.Name, path, err)
	}
	return nil
}
