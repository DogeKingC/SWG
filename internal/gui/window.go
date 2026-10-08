package gui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/Trlydev/SWG/internal/app"
	"github.com/Trlydev/SWG/internal/manager"
)

// browserCandidates lists Chromium-based browsers that support --app windows.
func browserCandidates() []string {
	if runtime.GOOS == "windows" {
		var out []string
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
			base := os.Getenv(env)
			if base == "" {
				continue
			}
			out = append(out,
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(base, `BraveSoftware\Brave-Browser\Application\brave.exe`),
			)
		}
		return out
	}
	var out []string
	for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
		if p, err := exec.LookPath(n); err == nil {
			out = append(out, p)
		}
	}
	return out
}

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
