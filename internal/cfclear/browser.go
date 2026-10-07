package cfclear

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// FindBrowser locates an Edge or Chrome executable. ppgmods uses the
// person's installed browser to view the page; it never bundles one.
// PPGMODS_BROWSER overrides the search with an explicit path.
func FindBrowser() (string, error) {
	if p := os.Getenv("PPGMODS_BROWSER"); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		pf, pf86, local := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")
		for _, base := range []string{pf86, pf, local} {
			if base == "" {
				continue
			}
			candidates = append(candidates,
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(base, "Chromium", "Application", "chrome.exe"),
			)
		}
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default:
		for _, name := range []string{
			"microsoft-edge", "microsoft-edge-stable", "google-chrome", "google-chrome-stable",
			"chromium", "chromium-browser", "brave-browser", "brave",
		} {
			if p, err := exec.LookPath(name); err == nil {
				return p, nil
			}
		}
		candidates = []string{
			"/usr/bin/microsoft-edge-stable", "/usr/bin/google-chrome-stable",
			"/usr/bin/chromium", "/usr/bin/chromium-browser", "/snap/bin/chromium",
			"/usr/bin/brave-browser",
		}
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("no Edge or Chrome browser found; install one, or set its path with PPGMODS_BROWSER")
}
