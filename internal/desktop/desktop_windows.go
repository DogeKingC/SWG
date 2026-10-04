//go:build windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const uninstallKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\ppgmods`

func installDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		h, _ := os.UserHomeDir()
		base = filepath.Join(h, "AppData", "Local")
	}
	return filepath.Join(base, "Programs", AppName)
}

// InstallPath is %LocalAppData%\Programs\PPG Mod Manager\ppgmods.exe.
func InstallPath() string { return filepath.Join(installDir(), "ppgmods.exe") }

func startMenuLink() string {
	return filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs`, AppName+".lnk")
}

func desktopLink() string {
	h, _ := os.UserHomeDir()
	if out, err := hidden("powershell", "-NoProfile", "-Command", "[Environment]::GetFolderPath('Desktop')").Output(); err == nil {
		if d := strings.TrimSpace(string(out)); d != "" {
			return filepath.Join(d, AppName+".lnk")
		}
	}
	return filepath.Join(h, "Desktop", AppName+".lnk")
}

// hidden runs a helper without flashing a console window.
func hidden(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return c
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func shortcut(link, exe string) error {
	os.MkdirAll(filepath.Dir(link), 0o755)
	script := fmt.Sprintf("$s=(New-Object -ComObject WScript.Shell).CreateShortcut(%s);$s.TargetPath=%s;$s.Arguments='gui';$s.WorkingDirectory=%s;$s.IconLocation=%s;$s.Description=%s;$s.Save()",
		psQuote(link), psQuote(exe), psQuote(filepath.Dir(exe)), psQuote(exe+",0"), psQuote("Recover, install and update People Playground mods"))
	out, err := hidden("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("creating shortcut: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func regSet(name, typ, value string) error {
	return hidden("reg", "add", uninstallKey, "/v", name, "/t", typ, "/d", value, "/f").Run()
}

// Install copies the program into the per-user Programs folder, adds Start
// Menu and (optionally) desktop shortcuts and an Apps & features entry. It
// returns the installed path.
func Install(o Options, logf func(string, ...any)) (string, error) {
	exe := InstallPath()
	if err := copyExe(exe); err != nil {
		return "", err
	}
	logf("installed %s", exe)
	if err := shortcut(startMenuLink(), exe); err != nil {
		return exe, err
	}
	logf("added %s to the Start menu", AppName)
	if o.DesktopShortcut {
		if err := shortcut(desktopLink(), exe); err != nil {
			logf("desktop shortcut: %v", err)
		} else {
			logf("added a desktop shortcut")
		}
	}
	regSet("DisplayName", "REG_SZ", AppName)
	regSet("DisplayVersion", "REG_SZ", strings.TrimPrefix(o.Version, "v"))
	regSet("Publisher", "REG_SZ", "Trlydev/SWG")
	regSet("DisplayIcon", "REG_SZ", exe+",0")
	regSet("InstallLocation", "REG_SZ", installDir())
	regSet("UninstallString", "REG_SZ", `"`+exe+`" uninstall-app`)
	regSet("URLInfoAbout", "REG_SZ", "https://github.com/Trlydev/SWG")
	regSet("NoModify", "REG_DWORD", "1")
	regSet("NoRepair", "REG_DWORD", "1")
	return exe, nil
}

// Uninstall removes the shortcuts, the Apps & features entry and the program
// folder (the running executable is deleted a moment after it exits). Mods
// and ppgmods' data folder are left alone.
func Uninstall(logf func(string, ...any)) error {
	for _, p := range []string{startMenuLink(), desktopLink()} {
		if err := os.Remove(p); err == nil {
			logf("removed %s", p)
		}
	}
	hidden("reg", "delete", uninstallKey, "/f").Run()
	dir := installDir()
	if strings.EqualFold(filepath.Dir(Executable()), dir) {
		// Windows cannot delete a running program: let cmd do it after we exit.
		return hidden("cmd", "/c", "ping -n 3 127.0.0.1 >nul & rmdir /s /q \""+dir+"\"").Start()
	}
	return os.RemoveAll(dir)
}

// Launch starts exe detached from this process.
func Launch(exe string, args ...string) error {
	c := exec.Command(exe, args...)
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200} // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	return c.Start()
}

const nxmKey = `HKCU\Software\Classes\nxm`

// RegisterNXM makes ppgmods open nxm:// links (Nexus Mods' "Mod Manager
// Download" button) for this user and returns the previous handler's
// command, to restore later.
func RegisterNXM(exe string) (string, error) {
	prev := ""
	if out, err := hidden("reg", "query", `HKCR\nxm\shell\open\command`, "/ve").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if i := strings.Index(line, "REG_SZ"); i >= 0 {
				prev = strings.TrimSpace(line[i+len("REG_SZ"):])
			}
		}
		if strings.Contains(strings.ToLower(prev), strings.ToLower(filepath.Base(exe))) {
			prev = "" // already ours
		}
	}
	cmd := `"` + exe + `" nxm "%1"`
	for _, args := range [][]string{
		{"add", nxmKey, "/ve", "/d", "URL:Nexus Mods link", "/f"},
		{"add", nxmKey, "/v", "URL Protocol", "/d", "", "/f"},
		{"add", nxmKey + `\shell\open\command`, "/ve", "/d", cmd, "/f"},
	} {
		if err := hidden("reg", args...).Run(); err != nil {
			return prev, fmt.Errorf("registering nxm links: %v", err)
		}
	}
	return prev, nil
}

// UnregisterNXM gives nxm:// links back to the previous handler.
func UnregisterNXM(prev string) {
	if prev != "" {
		hidden("reg", "add", nxmKey+`\shell\open\command`, "/ve", "/d", prev, "/f").Run()
		return
	}
	hidden("reg", "delete", nxmKey, "/f").Run()
}
