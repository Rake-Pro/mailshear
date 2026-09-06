// Command install builds mailshear, puts it on PATH and creates the default
// config file. Run it with: go run ./tools/install
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Rake-Pro/mailshear/internal/config"
)

func main() {
	noPath := flag.Bool("no-add-to-path", false, "do not add the Go bin directory to PATH")
	flag.Parse()

	root, err := moduleRoot()
	if err != nil {
		fail(err)
	}

	install := exec.Command("go", "install", "-trimpath", "-ldflags=-s -w", "./cmd/mailshear")
	install.Dir = root
	install.Env = append(os.Environ(), "CGO_ENABLED=0")
	install.Stdout, install.Stderr = os.Stdout, os.Stderr
	if err := install.Run(); err != nil {
		fail(fmt.Errorf("go install: %w", err))
	}

	bin := goBin()
	exe := filepath.Join(bin, "mailshear"+exeSuffix)
	fmt.Println("installed:", exe)

	if !*noPath {
		if err := ensurePath(bin); err != nil {
			fmt.Println("NOTE: could not update PATH:", err)
			fmt.Println("      add this directory yourself:", bin)
		}
	} else if !onPath(bin) {
		fmt.Println("NOTE:", bin, "is not on PATH")
	}

	if err := ensureConfig(); err != nil {
		fmt.Println("NOTE: could not create the config file:", err)
	}
}

func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", fmt.Errorf("run this from inside the mailshear repository")
	}
	return filepath.Dir(gomod), nil
}

func goEnv(name string) string {
	out, err := exec.Command("go", "env", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func goBin() string {
	if b := goEnv("GOBIN"); b != "" {
		return b
	}
	return filepath.Join(goEnv("GOPATH"), "bin")
}

func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if samePath(p, dir) {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if caseInsensitivePaths {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func ensureConfig() error {
	path := config.DefaultPath()
	if b, err := os.ReadFile(path); err == nil {
		// A BOM-only or whitespace-only file counts as missing.
		b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
		if len(bytes.TrimSpace(b)) > 0 {
			fmt.Println("config:", path)
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(config.Example()+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Println("config: created", path)
	fmt.Println("        run mailshear in a terminal; its setup screen fills this in")
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "install:", err)
	os.Exit(1)
}
