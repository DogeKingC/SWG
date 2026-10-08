package gui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Trlydev/SWG/internal/app"
	"github.com/Trlydev/SWG/internal/cfclear"
	"github.com/Trlydev/SWG/internal/manager"
)

// browserCandidates lists Chromium-based browsers that support --app windows.
func browserCandidates() []string { return cfclear.FindBrowsers() }

// openWindow shows url in an app-style window (no tabs or address bar) using
// a separate browser profile, or in the default browser as a fallback.
func openWindow(url string) error {
	profile := ""
	if d, err := manager.ConfigDir(); err == nil {
		profile = filepath.Join(d, "window-profile")
	}
	for _, b := range browserCandidates() {
		if _, err := os.Stat(b); err != nil {
			continue
		}
		args := []string{"--app=" + url, "--window-size=1200,820", "--no-first-run", "--no-default-browser-check"}
		if profile != "" {
			args = append(args, "--user-data-dir="+profile)
		}
		if err := exec.Command(b, args...).Start(); err == nil {
			return nil
		}
	}
	if err := app.OpenBrowser(url); err != nil {
		return errors.New("no browser found")
	}
	return nil
}
