package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openURL hands a URL to the system browser and returns without waiting for it.
func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening %s: %w", url, err)
	}
	// Reap the child without blocking the caller; the TUI keeps running.
	go func() { _ = cmd.Wait() }()
	return nil
}
