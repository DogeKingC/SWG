package manager

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Trlydev/SWG/internal/game"
)

// RE_PPG's approvals, laid out as under Proton: the first Verify takes
// what's there as known; a "Trust and run" approval that appears later is
// flagged once with its time; a file RE_PPG didn't write is flagged as
// forged; revoking deletes the unsafe and forged ones only.
func TestVerifyApprovals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lays out a Proton prefix")
	}
	t.Setenv("PPGMODS_HOME", t.TempDir())
	lib := t.TempDir()
	gameDir := filepath.Join(lib, "steamapps", "common", "People Playground")
	os.MkdirAll(filepath.Join(gameDir, "Mods"), 0o755)
	dir := filepath.Join(lib, "steamapps", "compatdata", game.AppID, "pfx", "drive_c", "users", "steamuser", "AppData", "Local", "RE_PPG", "local-mods", "0123456789ABCDEF0123", "launch-approvals")
	os.MkdirAll(dir, 0o755)
	fp := func(c string) string { return strings.Repeat(c, 64) }
	write := func(name, text string) { os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644) }
	write(fp("a"), game.ApprovalText)
	write(fp("b")+".unsafe", game.UnsafeApprovalText)
	write("notes.txt", "not an approval")
	m := &Manager{State: &State{Mods: map[string]*Installed{}}, ModsDir: filepath.Join(gameDir, "Mods")}

	bad := func(probs []Problem) (n int, text string) {
		for _, p := range probs {
			if p.Bad {
				n++
				text += p.Issue + "\n"
			}
		}
		return
	}
	if n, txt := bad(m.verifyApprovals(gameDir)); n != 0 {
		t.Fatalf("first Verify flagged what was already there:\n%s", txt)
	}
	write(fp("c")+".unsafe", game.UnsafeApprovalText)
	probs := m.verifyApprovals(gameDir)
	if n, txt := bad(probs); n != 1 || !strings.Contains(txt, "Trust and run") {
		t.Fatalf("a new Trust and run approval: %d flagged\n%s", n, txt)
	}
	if !strings.Contains(probs[len(probs)-1].Issue, "2 mod version(s)") {
		t.Errorf("count: %+v", probs[len(probs)-1])
	}
	if n, _ := bad(m.verifyApprovals(gameDir)); n != 0 {
		t.Error("the same approval was flagged twice")
	}
	write(fp("d"), "whatever the malware wrote")
	if n, txt := bad(m.verifyApprovals(gameDir)); n != 1 || !strings.Contains(txt, "didn't write") {
		t.Fatalf("forged approval: %d\n%s", n, txt)
	}
	n, err := m.RevokeUnsafeApprovals()
	if err != nil || n != 3 {
		t.Fatalf("revoked %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, fp("a"))); err != nil {
		t.Error("revoking deleted an approval with the security checks on")
	}
}

// A DataDirectory set in RE_PPG's BepInEx config is where its approvals are.
func TestApprovalDirsFromConfig(t *testing.T) {
	gameDir := t.TempDir()
	os.MkdirAll(filepath.Join(gameDir, "BepInEx", "config"), 0o755)
	os.WriteFile(filepath.Join(gameDir, "BepInEx", "config", "community.re_ppg.mods.cfg"), []byte("[General]\nDataDirectory = nope\n\n[Paths]\n# Compiler and updater working files.\nDataDirectory = rp-data\n"), 0o644)
	os.MkdirAll(filepath.Join(gameDir, "rp-data", "launch-approvals"), 0o755)
	got := game.ApprovalDirs(gameDir)
	if len(got) != 1 || got[0] != filepath.Join(gameDir, "rp-data", "launch-approvals") {
		t.Fatalf("got %v", got)
	}
}

// The first Verify with no approvals at all still records that, so an
// approval planted afterwards is flagged.
func TestVerifyApprovalsBaselineWhenEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lays out a Proton prefix")
	}
	t.Setenv("PPGMODS_HOME", t.TempDir())
	lib := t.TempDir()
	gameDir := filepath.Join(lib, "steamapps", "common", "People Playground")
	os.MkdirAll(filepath.Join(gameDir, "Mods"), 0o755)
	dir := filepath.Join(lib, "steamapps", "compatdata", game.AppID, "pfx", "drive_c", "users", "steamuser", "AppData", "Local", "RE_PPG", "local-mods", "AB", "launch-approvals")
	os.MkdirAll(dir, 0o755)
	m := &Manager{State: &State{Mods: map[string]*Installed{}}, ModsDir: filepath.Join(gameDir, "Mods")}
	if probs := m.verifyApprovals(gameDir); len(probs) != 0 {
		t.Fatalf("%+v", probs)
	}
	os.WriteFile(filepath.Join(dir, strings.Repeat("e", 64)+".unsafe"), []byte(game.UnsafeApprovalText), 0o644)
	flagged := false
	for _, p := range m.verifyApprovals(gameDir) {
		flagged = flagged || p.Bad
	}
	if !flagged {
		t.Fatal("an approval planted after an empty first Verify was not flagged")
	}
}
