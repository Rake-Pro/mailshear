// Command mailshear is a terminal UI for unsubscribing from bulk mail and
// clearing it out of an IMAP mailbox. It has no subcommands: everything it
// does is a screen, and it needs a terminal to run in.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/term"

	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/tui"
)

// version is set at build time with -ldflags "-X main.version=...";
// goreleaser fills it in from the release tag.
var version = "dev"

// Exit codes: 1 a runtime error, 2 usage or no terminal, 4 finished with
// senders that still need a manual unsubscribe.
const (
	exitError  = 1
	exitUsage  = 2
	exitManual = 4
)

var (
	flagConfig  string
	flagDataDir string
	flagVerbose bool
)

func usage(w io.Writer) {
	fmt.Fprintln(w, "mailshear is a terminal UI for unsubscribing from bulk mail and clearing it out of an IMAP mailbox.")
	fmt.Fprintf(w, "Flags: --config PATH (default %q), --data-dir PATH (default %q), -v/--verbose, --version, -h/--help.\n",
		config.DefaultPath(), config.DataDir())
}

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(args []string) int {
	fs := flag.NewFlagSet("mailshear", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { usage(os.Stdout) }
	showVersion := fs.Bool("version", false, "print the build version and exit")
	fs.StringVar(&flagConfig, "config", config.DefaultPath(), "path to config.yaml")
	fs.StringVar(&flagDataDir, "data-dir", config.DataDir(), "directory for the local database")
	fs.BoolVar(&flagVerbose, "verbose", false, "debug logging")
	fs.BoolVar(&flagVerbose, "v", false, "debug logging")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitUsage
	}
	if *showVersion {
		fmt.Fprintln(os.Stdout, version)
		return 0
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "mailshear: unexpected argument %q\n", fs.Arg(0))
		usage(os.Stderr)
		return exitUsage
	}

	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		fmt.Fprintln(os.Stderr, "mailshear is interactive; run it in a terminal")
		return exitUsage
	}

	setupLogging()
	if err := runFlow(); err != nil {
		var me manualError
		if errors.As(err, &me) {
			fmt.Fprintf(os.Stderr, "mailshear: %v\n", err)
			return exitManual
		}
		fmt.Fprintf(os.Stderr, "mailshear: %v\n", err)
		return exitError
	}
	return 0
}

// manualError ends a session that finished with senders needing a browser, so
// a caller watching the exit code can tell that apart from a failure.
type manualError struct{ n int }

func (e manualError) Error() string {
	return fmt.Sprintf("%d sender(s) need a manual unsubscribe", e.n)
}

func runFlow() error {
	// A missing or unreadable config is not an error: the setup screen is
	// what fixes it.
	cfg, _ := config.Load(flagConfig)

	backend := tui.NewBackend(cfg, flagConfig, flagDataDir)
	backend.OpenURL = openURL
	backend.Version = version
	defer backend.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := tui.RunFlow(ctx, tui.FlowDeps{
		Cfg:        cfg,
		ConfigPath: flagConfig,
		DataDir:    flagDataDir,
		Backend:    backend,
		OpenURL:    openURL,
		Version:    version,
	})
	if err != nil {
		return err
	}
	if res.Manual > 0 {
		return manualError{n: res.Manual}
	}
	return nil
}

func setupLogging() {
	level := zerolog.InfoLevel
	if flagVerbose {
		level = zerolog.DebugLevel
	}
	zerolog.SetGlobalLevel(level)
	// The UI owns the terminal, so anything logged goes to stderr and is only
	// seen once the program has exited or stderr has been redirected.
	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	}).With().Timestamp().Logger()
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
