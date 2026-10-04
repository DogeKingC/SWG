//go:build !windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func home() string { h, _ := os.UserHomeDir(); return h }

func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	return filepath.Join(home(), ".local", "share")
}

// InstallPath is ~/.local/bin/ppgmods.
func InstallPath() string { return filepath.Join(home(), ".local", "bin", "ppgmods") }

func desktopDir() string {
	if out, err := exec.Command("xdg-user-dir", "DESKTOP").Output(); err == nil {
		if d := strings.TrimSpace(string(out)); d != "" && d != home() {
			return d
		}
	}
	return filepath.Join(home(), "Desktop")
}

func entry(exe string) string {
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
GenericName=People Playground mod manager
Comment=Recover, install and update People Playground mods safely
Exec="%s" gui
Icon=ppgmods
Terminal=false
Categories=Game;Utility;
Keywords=People Playground;PPG;mods;workshop;
StartupNotify=true
`, AppName, exe)
}

func paths() (menu, desk, png, svg string) {
	d := dataHome()
	return filepath.Join(d, "applications", "ppgmods.desktop"),
		filepath.Join(desktopDir(), "ppgmods.desktop"),
		filepath.Join(d, "icons", "hicolor", "256x256", "apps", "ppgmods.png"),
		filepath.Join(d, "icons", "hicolor", "scalable", "apps", "ppgmods.svg")
}

// Install copies the program to ~/.local/bin and adds an app-menu entry, an
// icon and optionally a desktop shortcut. It returns the installed path.
func Install(o Options, logf func(string, ...any)) (string, error) {
	exe := InstallPath()
	if err := copyExe(exe); err != nil {
		return "", err
	}
	logf("installed %s", exe)
	menu, desk, png, svg := paths()
	for p, b := range map[string][]byte{png: iconPNG, svg: iconSVG} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return exe, err
		}
	}
	os.MkdirAll(filepath.Dir(menu), 0o755)
	if err := os.WriteFile(menu, []byte(entry(exe)), 0o755); err != nil {
		return exe, err
	}
	logf("added %s to the application menu", AppName)
	if o.DesktopShortcut {
		if err := os.MkdirAll(filepath.Dir(desk), 0o755); err == nil {
			if err := os.WriteFile(desk, []byte(entry(exe)), 0o755); err == nil {
				// GNOME only launches desktop files marked trusted.
				exec.Command("gio", "set", desk, "metadata::trusted", "true").Run()
				logf("added a desktop shortcut")
			}
		}
	}
	exec.Command("update-desktop-database", filepath.Dir(menu)).Run()
	exec.Command("gtk-update-icon-cache", "-q", filepath.Join(dataHome(), "icons", "hicolor")).Run()
	return exe, nil
}

// Uninstall removes the program, menu entry, icons and desktop shortcut.
// Mods and ppgmods' data folder are left alone.
func Uninstall(logf func(string, ...any)) error {
	menu, desk, png, svg := paths()
	for _, p := range []string{menu, desk, png, svg, InstallPath(), InstallPath() + ".old"} {
		if err := os.Remove(p); err == nil {
			logf("removed %s", p)
		}
	}
	exec.Command("update-desktop-database", filepath.Dir(menu)).Run()
	return nil
}

// Launch starts exe detached from this process.
func Launch(exe string, args ...string) error {
	c := exec.Command(exe, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return c.Start()
}

const nxmDesktop = "ppgmods-nxm.desktop"

// RegisterNXM makes ppgmods open nxm:// links (Nexus Mods' "Mod Manager
// Download" button) and returns the previous handler, to restore later.
func RegisterNXM(exe string) (string, error) {
	prev := ""
	if out, err := exec.Command("xdg-mime", "query", "default", "x-scheme-handler/nxm").Output(); err == nil {
		if p := strings.TrimSpace(string(out)); p != nxmDesktop {
			prev = p
		}
	}
	file := filepath.Join(dataHome(), "applications", nxmDesktop)
	os.MkdirAll(filepath.Dir(file), 0o755)
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s (Nexus Mods links)
Exec="%s" nxm %%u
Icon=ppgmods
NoDisplay=true
Terminal=false
MimeType=x-scheme-handler/nxm;
`, AppName, exe)
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		return prev, err
	}
	exec.Command("update-desktop-database", filepath.Dir(file)).Run()
	if err := exec.Command("xdg-mime", "default", nxmDesktop, "x-scheme-handler/nxm").Run(); err != nil {
		return prev, fmt.Errorf("xdg-mime: %v", err)
	}
	return prev, nil
}

// UnregisterNXM gives nxm:// links back to the previous handler.
func UnregisterNXM(prev string) {
	file := filepath.Join(dataHome(), "applications", nxmDesktop)
	os.Remove(file)
	if prev != "" {
		exec.Command("xdg-mime", "default", prev, "x-scheme-handler/nxm").Run()
	}
	exec.Command("update-desktop-database", filepath.Dir(file)).Run()
}
