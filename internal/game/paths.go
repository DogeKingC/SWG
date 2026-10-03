// Package game locates the People Playground install and the Steam Workshop
// cache on Windows and Linux.
package game

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const AppID = "1118200"

var reVDFPath = regexp.MustCompile(`"path"\s+"([^"]+)"`)

// SteamRoots returns plausible Steam install folders for this OS.
func SteamRoots() []string {
	var roots []string
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
			if v := os.Getenv(env); v != "" {
				roots = append(roots, filepath.Join(v, "Steam"))
			}
		}
		roots = append(roots, `C:\Program Files (x86)\Steam`)
	} else {
		roots = append(roots,
			filepath.Join(home, ".steam", "steam"),
			filepath.Join(home, ".local", "share", "Steam"),
			filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"),
			filepath.Join(home, "snap", "steam", "common", ".local", "share", "Steam"),
		)
	}
	return existing(roots)
}

// Libraries returns every Steam library folder (each holds a steamapps dir).
func Libraries() []string {
	seen := map[string]bool{}
	var libs []string
	add := func(p string) {
		if abs, err := filepath.EvalSymlinks(p); err == nil {
			p = abs
		}
		if !seen[p] {
			seen[p] = true
			libs = append(libs, p)
		}
	}
	for _, root := range SteamRoots() {
		add(root)
		b, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
		if err != nil {
			continue
		}
		for _, m := range reVDFPath.FindAllStringSubmatch(string(b), -1) {
			p := strings.ReplaceAll(m[1], `\\`, `\`)
			if _, err := os.Stat(filepath.Join(p, "steamapps")); err == nil {
				add(p)
			}
		}
	}
	return libs
}

// FindGameDir returns the People Playground install folder. override (from a
// flag or the PPG_DIR environment variable) wins when set.
func FindGameDir(override string) (string, error) {
	if override == "" {
		override = os.Getenv("PPG_DIR")
	}
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", err
		}
		return override, nil
	}
	for _, lib := range Libraries() {
		p := filepath.Join(lib, "steamapps", "common", "People Playground")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("People Playground not found; pass --game <folder> or set PPG_DIR")
}

// WorkshopDirs returns existing Workshop cache folders for People Playground.
func WorkshopDirs() []string {
	var out []string
	for _, lib := range Libraries() {
		out = append(out, filepath.Join(lib, "steamapps", "workshop", "content", AppID))
	}
	return existing(out)
}

func ModsDir(gameDir string) string { return filepath.Join(gameDir, "Mods") }

// ContraptionsDir is where the game keeps saved contraptions.
func ContraptionsDir(gameDir string) string { return filepath.Join(gameDir, "Contraptions") }

func existing(ps []string) []string {
	var out []string
	for _, p := range ps {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}
