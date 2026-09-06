package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/term"
)

// --version prints the build version and stops, whether or not there is a
// terminal, so a script can still ask what it is running.
func TestVersionFlagPrintsTheVersion(t *testing.T) {
	out := captureStdout(t, func() {
		if code := run([]string{"--version"}); code != 0 {
			t.Fatalf("run(--version) = %d, want 0", code)
		}
	})
	if strings.TrimSpace(out) != version {
		t.Fatalf("printed %q, want %q", strings.TrimSpace(out), version)
	}
}

// Everything else needs a terminal: mailshear is one interactive program, so a
// pipe or a cron job gets a usage error rather than a half-started UI.
func TestNonTerminalExitsTwo(t *testing.T) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal; this test needs a pipe")
	}
	if code := run(nil); code != exitUsage {
		t.Fatalf("run(nil) = %d, want %d", code, exitUsage)
	}
}

// An unknown argument is a usage error: there are no subcommands to mistype.
func TestUnknownArgumentExitsTwo(t *testing.T) {
	if code := run([]string{"scan"}); code != exitUsage {
		t.Fatalf("run(scan) = %d, want %d", code, exitUsage)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	fn()
	w.Close()

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return sb.String()
}

// The built binary prints the two-line help and exits 0 for -h, which is the
// only other thing it does without a terminal.
func TestHelpExitsZero(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH")
	}
	bin := filepath.Join(t.TempDir(), "mailshear")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("mailshear -h: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "terminal UI") || !strings.Contains(string(out), "--data-dir") {
		t.Fatalf("help text = %q", out)
	}
}
