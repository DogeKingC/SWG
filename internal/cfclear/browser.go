package cfclear

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ErrNoBrowser explains why no browser could be used. The background check
// drives the browser through Chrome's DevTools protocol, which only
// Chromium-based browsers speak; Firefox-based ones (Firefox, Zen,
// LibreWolf, Floorp, Waterfox) can't be used for it.
var ErrNoBrowser = errors.New("no Chromium-based browser found (Chrome, Edge, Brave, Vivaldi or Chromium). " +
	"Firefox-based browsers such as Zen or Firefox can't run this check; install one of those " +
	"(it doesn't have to be your default browser), or set its path with PPGMODS_BROWSER")

// FindBrowser locates a Chromium-based browser. ppgmods uses the person's
// installed browser to view the page; it never bundles one.
// PPGMODS_BROWSER overrides the search with an explicit path.
func FindBrowser() (string, error) {
	if all := FindBrowsers(); len(all) > 0 {
		return all[0], nil
	}
	return "", ErrNoBrowser
}

// FindBrowsers lists the installed Chromium-based browsers, most suitable
// first: PPGMODS_BROWSER, then Edge and Chrome, then the others.
func FindBrowsers() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(os.Getenv("PPGMODS_BROWSER"))
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		pf, pf86, local := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")
		for _, rel := range []string{
			`Microsoft\Edge\Application\msedge.exe`,
			`Google\Chrome\Application\chrome.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
			`Vivaldi\Application\vivaldi.exe`,
			`Chromium\Application\chrome.exe`,
			`Microsoft\Edge Beta\Application\msedge.exe`,
			`Microsoft\Edge Dev\Application\msedge.exe`,
			`Google\Chrome Beta\Application\chrome.exe`,
			`BraveSoftware\Brave-Browser-Beta\Application\brave.exe`,
		} {
			for _, base := range []string{pf86, pf, local} {
				if base != "" {
					add(filepath.Join(base, rel))
				}
			}
		}
	case "darwin":
		for _, app := range []string{
			"Google Chrome", "Microsoft Edge", "Brave Browser", "Vivaldi", "Chromium",
			"Google Chrome Beta", "Microsoft Edge Beta", "Brave Browser Beta",
		} {
			for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
				add(filepath.Join(dir, app+".app", "Contents", "MacOS", app))
			}
		}
	default:
		for _, name := range []string{
			"microsoft-edge", "microsoft-edge-stable", "google-chrome", "google-chrome-stable",
			"chromium", "chromium-browser", "brave-browser", "brave", "brave-browser-stable",
			"vivaldi", "vivaldi-stable", "ungoogled-chromium", "thorium-browser",
			"microsoft-edge-beta", "microsoft-edge-dev", "google-chrome-beta", "google-chrome-unstable",
			"brave-browser-beta",
		} {
			if p, err := exec.LookPath(name); err == nil {
				add(p)
			}
		}
		for _, p := range []string{
			"/opt/microsoft/msedge/msedge", "/opt/google/chrome/chrome", "/opt/brave.com/brave/brave",
			"/opt/vivaldi/vivaldi", "/usr/lib/chromium/chromium", "/usr/lib/chromium-browser/chromium-browser",
			"/snap/bin/chromium", "/snap/bin/brave",
		} {
			add(p)
		}
		// Flatpak installs, last: their sandbox may keep the check from
		// working, but trying beats giving up.
		for _, dir := range []string{"/var/lib/flatpak/exports/bin", filepath.Join(home, ".local", "share", "flatpak", "exports", "bin")} {
			for _, id := range []string{"com.microsoft.Edge", "com.google.Chrome", "com.brave.Browser",
				"com.vivaldi.Vivaldi", "org.chromium.Chromium", "io.github.ungoogled_software.ungoogled_chromium"} {
				add(filepath.Join(dir, id))
			}
		}
	}
	return out
}
