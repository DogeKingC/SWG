package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quarantineSetup(t *testing.T) (*Manager, string) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	mods := filepath.Join(t.TempDir(), "Mods")
	dir := filepath.Join(mods, "Bad Mod")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "mod.json"), []byte(`{"Name":"Bad Mod","Scripts":["s.cs"]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "s.cs"), []byte("class S {}"), 0o644)
	files := map[string]string{}
	if err := hashTree(dir, "Bad Mod", files); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState()
	st.Mods["gb:1"] = &Installed{Key: "gb:1", Name: "Bad Mod", Folders: []string{"Bad Mod"}, Files: files}
	return &Manager{ModsDir: mods, State: st}, dir
}

// Quarantine takes a mod out of the game folder without deleting it, and
// Release puts it back unchanged.
func TestQuarantineAndRelease(t *testing.T) {
	m, dir := quarantineSetup(t)
	if err := m.Quarantine("gb:1", "worm suspected"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the folder is still in Mods/")
	}
	if m.MissingFolders(m.State.Mods["gb:1"]) {
		t.Error("a quarantined item shows as missing")
	}
	probs, _ := m.Verify()
	for _, p := range probs {
		if p.Bad {
			t.Errorf("quarantined item reported as a problem: %+v", p)
		}
	}
	// Updates, repairs and rollbacks can't bring it back by themselves.
	if err := m.Rollback("gb:1"); err == nil || !strings.Contains(err.Error(), "quarantine") {
		t.Errorf("rollback of a quarantined item: %v", err)
	}
	if err := m.Install(&Candidate{Key: "gb:1", Path: dir}); err == nil || !strings.Contains(err.Error(), "quarantine") {
		// Stage fails first on the missing path in this setup; either way it
		// must not install.
		if _, serr := os.Stat(dir); serr == nil {
			t.Errorf("install brought a quarantined item back: %v", err)
		}
	}

	if err := m.Release("gb:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s.cs")); err != nil {
		t.Fatal("release did not put the files back")
	}
	probs, _ = m.Verify()
	for _, p := range probs {
		if p.Bad {
			t.Errorf("released item reported: %+v", p)
		}
	}
}

// A folder that reappears under a quarantined mod's name is reported, and
// Release refuses to overwrite it.
func TestQuarantinedFolderReappearing(t *testing.T) {
	m, dir := quarantineSetup(t)
	if err := m.Quarantine("gb:1", "x"); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "s.cs"), []byte("class Worm {}"), 0o644)
	probs, _ := m.Verify()
	found := false
	for _, p := range probs {
		if p.Bad && strings.Contains(p.Issue, "back in the game folder") {
			found = true
		}
	}
	if !found {
		t.Errorf("reappearing folder not reported: %+v", probs)
	}
	if err := m.Release("gb:1"); err == nil {
		t.Error("release overwrote a folder that reappeared")
	}
}

// Removing a quarantined item deletes its quarantined copy.
func TestRemoveQuarantined(t *testing.T) {
	m, _ := quarantineSetup(t)
	if err := m.Quarantine("gb:1", "x"); err != nil {
		t.Fatal(err)
	}
	q, _ := m.quarantineDir(m.State.Mods["gb:1"])
	if err := m.Remove("gb:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(q); err == nil {
		t.Error("the quarantined copy was left behind")
	}
	if m.State.Mods["gb:1"] != nil {
		t.Error("still tracked after remove")
	}
}
