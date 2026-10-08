package game

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// RE_PPG asks before it loads a C# mod and remembers the answer per mod
// version: a file named after the version's fingerprint (a SHA-256 of the
// mod's paths and script contents) in <data>/launch-approvals. A plain
// name means "load it, security filters on"; a ".unsafe" suffix means the
// person clicked "Trust and run": that version runs WITHOUT RE_PPG's
// security checks. <data> is RE_PPG's DataDirectory (its BepInEx config),
// by default %LOCALAPPDATA%\RE_PPG\local-mods\<hash of the game path>;
// under Proton, %LOCALAPPDATA% is inside the game's prefix.

// The exact text RE_PPG writes into an approval file.
const (
	ApprovalText       = "Launch approved; security filters remain enabled."
	UnsafeApprovalText = "User explicitly approved this source version with compiler security checks disabled."
)

// ApprovalDirs returns the existing RE_PPG launch-approvals folders for the
// game in gameDir.
func ApprovalDirs(gameDir string) []string {
	var datas []string
	if d := reppgConfiguredData(gameDir); d != "" {
		datas = append(datas, d)
	}
	for _, local := range localAppData(gameDir) {
		ms, _ := filepath.Glob(filepath.Join(local, "RE_PPG", "local-mods", "*"))
		datas = append(datas, ms...)
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range datas {
		p := filepath.Join(d, "launch-approvals")
		if seen[p] {
			continue
		}
		seen[p] = true
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// localAppData lists where %LOCALAPPDATA% may be for the game: the user's
// own on Windows, the Proton prefix's elsewhere.
func localAppData(gameDir string) []string {
	if runtime.GOOS == "windows" {
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return []string{v}
		}
		return nil
	}
	// <library>/steamapps/common/People Playground -> <library>/steamapps
	steamapps := filepath.Dir(filepath.Dir(gameDir))
	return []string{filepath.Join(steamapps, "compatdata", AppID, "pfx", "drive_c", "users", "steamuser", "AppData", "Local")}
}

// reppgConfigName is RE_PPG's BepInEx config file (its plugin GUID).
const reppgConfigName = "community.re_ppg.mods.cfg"

// reppgConfiguredData reads a DataDirectory set in RE_PPG's BepInEx config
// (a relative path is under the game folder; under Proton, Z:\ is /).
// Other plugins' configs are never read.
func reppgConfiguredData(gameDir string) string {
	for _, cfg := range []string{filepath.Join(gameDir, "BepInEx", "config", reppgConfigName)} {
		f, err := os.Open(cfg)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		section := ""
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = line
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok || section != "[Paths]" || strings.TrimSpace(k) != "DataDirectory" {
				continue
			}
			v = strings.TrimSpace(v)
			f.Close()
			return nativePath(gameDir, v)
		}
		f.Close()
	}
	return ""
}

func nativePath(gameDir, p string) string {
	if p == "" {
		return ""
	}
	if runtime.GOOS != "windows" {
		if len(p) >= 3 && (p[0] == 'Z' || p[0] == 'z') && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
			return filepath.FromSlash(strings.ReplaceAll(p[2:], `\`, "/"))
		}
		if len(p) >= 2 && p[1] == ':' {
			return "" // another Windows drive inside the prefix: not mapped
		}
		p = strings.ReplaceAll(p, `\`, "/")
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(gameDir, p)
}
