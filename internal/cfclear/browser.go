package cfclear

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNoBrowser means no usable browser is installed.
var ErrNoBrowser = errors.New("no browser found for the check: install Chrome, Edge, Brave, Vivaldi, Chromium, " +
	"Firefox or Zen (it doesn't have to be your default browser), or set its path with PPGMODS_BROWSER")

// FindBrowser locates a browser for the background check: a Chromium-based
// one first (driven through Chrome's DevTools protocol), else a
// Firefox-based one (driven through WebDriver BiDi). ppgmods uses the
// person's installed browser; it never bundles one. PPGMODS_BROWSER
// overrides the search with an explicit path.
func FindBrowser() (string, error) {
	if all := FindBrowsers(); len(all) > 0 {
		return all[0], nil
	}
	if all := firefoxBrowsers(); len(all) > 0 {
		return all[0], nil
	}
	return "", ErrNoBrowser
}

// firefoxBrowsers lists installed Firefox-based browsers.
func firefoxBrowsers() []string {
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
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		pf, pf86, local := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")
		for _, rel := range []string{
			`Mozilla Firefox\firefox.exe`, `Zen Browser\zen.exe`, `Zen\zen.exe`, `LibreWolf\librewolf.exe`,
			`Floorp\floorp.exe`, `Waterfox\waterfox.exe`,
		} {
			for _, base := range []string{pf, pf86, local, filepath.Join(local, "Programs")} {
				if base != "" {
					add(filepath.Join(base, rel))
				}
			}
		}
	case "darwin":
		for _, app := range [][2]string{{"Firefox", "firefox"}, {"Zen", "zen"}, {"Zen Browser", "zen"},
			{"LibreWolf", "librewolf"}, {"Floorp", "floorp"}, {"Waterfox", "waterfox"}} {
			for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
				add(filepath.Join(dir, app[0]+".app", "Contents", "MacOS", app[1]))
			}
		}
	default:
		for _, name := range []string{"firefox", "zen-browser", "zen", "librewolf", "floorp", "waterfox", "firefox-esr"} {
			if p, err := exec.LookPath(name); err == nil {
				add(p)
			}
		}
		for _, p := range []string{"/opt/zen-browser/zen", "/opt/zen/zen", "/opt/firefox/firefox", "/usr/lib/firefox/firefox",
			"/usr/lib/librewolf/librewolf", "/snap/bin/firefox"} {
			add(p)
		}
		for _, dir := range []string{"/var/lib/flatpak/exports/bin", filepath.Join(home, ".local", "share", "flatpak", "exports", "bin")} {
			for _, id := range []string{"app.zen_browser.zen", "io.github.zen_browser.zen", "org.mozilla.firefox", "io.gitlab.librewolf-community", "one.ablaze.floorp"} {
				add(filepath.Join(dir, id))
			}
		}
	}
	return out
}

// sandboxProfileBase is where a Snap or Flatpak browser can read a profile
// (its sandbox hides /tmp), or "" for other installs.
func sandboxProfileBase(exe string) string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	if strings.HasPrefix(exe, "/snap/bin/") {
		return filepath.Join(home, "snap", filepath.Base(exe), "common")
	}
	if strings.Contains(exe, "/flatpak/exports/bin/") {
		return filepath.Join(home, ".var", "app", filepath.Base(exe), "cache")
	}
	return ""
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
