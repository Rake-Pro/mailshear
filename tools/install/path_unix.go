//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	exeSuffix            = ""
	caseInsensitivePaths = false
)

// ensurePath appends dir to the user's shell rc file when it is not on PATH.
func ensurePath(dir string) error {
	if onPath(dir) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	rc := filepath.Join(home, ".profile")
	switch shell := filepath.Base(os.Getenv("SHELL")); shell {
	case "zsh":
		rc = filepath.Join(home, ".zshrc")
	case "bash":
		if runtime.GOOS == "darwin" {
			rc = filepath.Join(home, ".bash_profile")
		} else {
			rc = filepath.Join(home, ".bashrc")
		}
	}
	line := fmt.Sprintf("export PATH=%q:$PATH", dir)
	if b, err := os.ReadFile(rc); err == nil && strings.Contains(string(b), line) {
		return nil
	}
	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n# added by mailshear tools/install\n%s\n", line); err != nil {
		return err
	}
	fmt.Println("added", dir, "to PATH in", rc, "(open a new terminal)")
	return nil
}
