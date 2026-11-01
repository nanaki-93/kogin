package main

import (
	"io"
	"os/exec"
	"runtime"
)

// openBrowser opens page with the system's default browser.
func openBrowser(page string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", page)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", page)
	default:
		cmd = exec.Command("xdg-open", page)
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Start()
}
