// Package desktop installs ppgmods as a regular desktop application: a fixed
// per-user location, Start Menu / app-menu entries, a desktop shortcut and
// (on Windows) an Apps & features uninstall entry. No administrator rights
// are needed. Self-updates replace the installed copy in place.
package desktop

import (
	_ "embed"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const AppName = "PPG Mod Manager"

//go:embed icons/icon-256.png
var iconPNG []byte

//go:embed icons/icon.svg
var iconSVG []byte

// IconPNG is the app icon (256×256).
func IconPNG() []byte { return iconPNG }

type Options struct {
	DesktopShortcut bool
	Version         string
}

var startedAs = Executable()

// StartedAs is the executable path this process was started from. Unlike
// Executable, it stays correct after a self-update renamed the running file.
func StartedAs() string { return startedAs }

// Executable returns the running executable's resolved path.
func Executable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe
}

// IsInstalled reports whether the running executable is the installed copy.
func IsInstalled() bool {
	exe, target := StartedAs(), InstallPath()
	if exe == "" || target == "" {
		return false
	}
	if r, err := filepath.EvalSymlinks(target); err == nil {
		target = r
	}
	return sameFile(exe, target)
}

// InstalledCopyExists reports whether an installed copy is present, whether
// or not it is the one running.
func InstalledCopyExists() bool {
	_, err := os.Stat(InstallPath())
	return err == nil
}

func sameFile(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	if err1 != nil || err2 != nil {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return os.SameFile(sa, sb)
}

// copyExe copies the running executable to dst (atomically via a temp file).
func copyExe(dst string) error {
	src := Executable()
	if sameFile(src, dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		old := dst + ".old"
		os.Remove(old)
		if err := os.Rename(dst, old); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return os.Rename(tmp, dst)
}
