package manager

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// installTestMod installs a small mod and returns its folder in Mods/.
func installTestMod(t *testing.T, m *Manager, key, folder string) string {
	t.Helper()
	src := zipOf(t, map[string]string{
		folder + "/mod.json": `{"Name":"` + folder + `","Author":"a","Scripts":["s.cs"]}`,
		folder + "/s.cs":     "class S {}",
	})
	if err := m.Install(&Candidate{Key: key, Name: folder, Path: src}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(m.ModsDir, m.State.Mods[key].Folders[0])
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Turning a mod off moves it out of Mods/ and back on puts it back; updates,
// rollbacks and Verify treat it as deliberately out, and removing it deletes
// its folders from the "off" area.
func TestTurnOffAndOn(t *testing.T) {
	m := newTestManager(t)
	alpha := installTestMod(t, m, "gb:1", "Alpha")
	if err := m.TurnOff("gb:1"); err != nil {
		t.Fatal(err)
	}
	if exists(alpha) || !m.State.Mods["gb:1"].Off {
		t.Fatal("still in the game folder after turning it off")
	}
	if m.MissingFolders(m.State.Mods["gb:1"]) {
		t.Fatal("a turned-off mod counts as missing")
	}
	probs, _ := m.Verify()
	for _, p := range probs {
		if p.Bad {
			t.Fatalf("verify: %+v", p)
		}
	}
	var rej *Rejection
	if err := m.Install(&Candidate{Key: "gb:1", Name: "Alpha", Path: zipOf(t, map[string]string{"Alpha/mod.json": `{"Name":"Alpha","Scripts":[]}`})}); !errors.As(err, &rej) {
		t.Fatalf("an update of a turned-off mod went through: %v", err)
	}
	// Something else puts a folder of that name back: Verify says so.
	os.MkdirAll(alpha, 0o755)
	probs, _ = m.Verify()
	if !slices.ContainsFunc(probs, func(p Problem) bool { return p.Bad && p.Key == "gb:1" }) {
		t.Fatalf("a folder back under a turned-off mod's name was not reported: %+v", probs)
	}
	if err := m.TurnOn("gb:1"); err == nil {
		t.Fatal("turned on over a folder that is in the way")
	}
	os.RemoveAll(alpha)
	if err := m.TurnOn("gb:1"); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(alpha, "s.cs")) || m.State.Mods["gb:1"].Off {
		t.Fatal("not back in the game folder")
	}
	probs, _ = m.Verify()
	for _, p := range probs {
		if p.Bad {
			t.Fatalf("verify after turning on: %+v", p)
		}
	}
	m.TurnOff("gb:1")
	if err := m.Remove("gb:1"); err != nil {
		t.Fatal(err)
	}
	d, _ := m.offDir(&Installed{Key: "gb:1"})
	if exists(d) || m.State.Mods["gb:1"] != nil {
		t.Fatal("removing a turned-off mod left its folders or its entry")
	}
}

// A profile saves which mods are on; using another turns mods on and off
// to match, leaves quarantined mods and contraptions alone, and names the
// profile's mods that aren't installed.
func TestProfiles(t *testing.T) {
	m := newTestManager(t)
	alpha := installTestMod(t, m, "gb:1", "Alpha")
	beta := installTestMod(t, m, "gb:2", "Beta")
	installTestMod(t, m, "gb:3", "Gamma")
	if err := m.Quarantine("gb:3", "test"); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveProfile("Everything"); err != nil {
		t.Fatal(err)
	}
	if got := m.State.Profiles["Everything"]; !slices.Equal(got, []string{"gb:1", "gb:2"}) {
		t.Fatalf("saved %v; quarantined mods are not part of a profile", got)
	}
	m.TurnOff("gb:2")
	if err := m.SaveProfile("Just Alpha"); err != nil {
		t.Fatal(err)
	}
	m.State.Profiles["Just Alpha"] = append(m.State.Profiles["Just Alpha"], "gb:99")
	if _, err := m.UseProfile("Everything"); err != nil {
		t.Fatal(err)
	}
	if !exists(beta) || m.State.Profile != "Everything" {
		t.Fatal("Beta not back on")
	}
	missing, err := m.UseProfile("Just Alpha")
	if err != nil {
		t.Fatal(err)
	}
	if exists(beta) || !exists(alpha) {
		t.Fatal("profile not applied")
	}
	if !slices.Equal(missing, []string{"gb:99"}) {
		t.Fatalf("missing %v", missing)
	}
	if m.State.Mods["gb:3"].Off || m.State.Mods["gb:3"].Quarantined == "" {
		t.Fatal("a quarantined mod was touched")
	}
	// Profiles survive a reload.
	st, err := LoadState()
	if err != nil || st.Profile != "Just Alpha" || !slices.Equal(st.ProfileNames(), []string{"Everything", "Just Alpha"}) {
		t.Fatalf("reloaded: %v %+v", err, st)
	}
	if err := m.DeleteProfile("Just Alpha"); err != nil || m.State.Profile != "" {
		t.Fatal("delete")
	}
	if _, err := m.UseProfile("nope"); err == nil {
		t.Fatal("used a profile that doesn't exist")
	}
	if err := m.SaveProfile("  "); err == nil {
		t.Fatal("saved a profile without a name")
	}
}

// Review fixes: a turned-off mod is kept next to Mods in the game folder
// (one drive, moved by rename); one changed while off stays off; one turned
// off by 0.3.0 (kept in the config folder) still turns on; quarantine and
// forget refuse a turned-off mod instead of orphaning its folders.
func TestTurnOffReviewFixes(t *testing.T) {
	m := newTestManager(t)
	alpha := installTestMod(t, m, "gb:1", "Alpha")
	if err := m.TurnOff("gb:1"); err != nil {
		t.Fatal(err)
	}
	d, _ := m.offDir(m.State.Mods["gb:1"])
	if filepath.Dir(filepath.Dir(d)) != filepath.Dir(m.ModsDir) {
		t.Fatalf("off folder %s is not next to Mods", d)
	}
	if err := m.Quarantine("gb:1", "x"); err == nil {
		t.Error("quarantined a turned-off mod")
	}
	if err := m.Forget("gb:1"); err == nil {
		t.Error("forgot a turned-off mod, orphaning its folders")
	}
	// Tampered with while off: stays off.
	s := filepath.Join(d, filepath.Base(alpha), "s.cs")
	os.WriteFile(s, []byte("class S { /* injected */ }"), 0o644)
	var rej *Rejection
	if err := m.TurnOn("gb:1"); !errors.As(err, &rej) || exists(alpha) {
		t.Fatalf("a mod changed while off was turned on: %v", err)
	}
	os.WriteFile(s, []byte("class S {}"), 0o644)
	if err := m.TurnOn("gb:1"); err != nil || !exists(alpha) {
		t.Fatalf("turn on: %v", err)
	}

	// Turned off by 0.3.0: its folders are in the config folder.
	inst := m.State.Mods["gb:1"]
	old := m.legacyOffDir(inst)
	os.MkdirAll(old, 0o755)
	if err := os.Rename(alpha, filepath.Join(old, filepath.Base(alpha))); err != nil {
		t.Fatal(err)
	}
	inst.Off = true
	if err := m.TurnOn("gb:1"); err != nil || !exists(filepath.Join(alpha, "s.cs")) || exists(old) {
		t.Fatalf("a mod turned off by 0.3.0 didn't come back: %v", err)
	}
}
