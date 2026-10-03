package manager

import (
	"archive/zip"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func zipOf(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	for n, b := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(b))
	}
	zw.Close()
	f.Close()
	return p
}

func listDir(t *testing.T, d string) []string {
	ents, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func newTestManager(t *testing.T) *Manager {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	st, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	game := t.TempDir()
	return &Manager{State: st, ModsDir: filepath.Join(game, "Mods"), ContraptionsDir: filepath.Join(game, "Contraptions")}
}

func TestInstallFlatContraption(t *testing.T) {
	m := newTestManager(t)
	// True Workshop style: flat files, odd capitalisation, plus a stray file.
	src := zipOf(t, map[string]string{
		"top secret town.jaap":    "\x02\x02data",
		"top secret town.json":    `{"Name":"top secret town"}`,
		"top secret town.outline": "o",
		"Top Secret Town.png":     "\x89PNG\r\n\x1a\n",
		"readme.txt":              "hi",
	})
	if err := m.Install(&Candidate{Key: "tw:168", Name: "Top Secret Town", Path: src}); err != nil {
		t.Fatal(err)
	}
	got := listDir(t, filepath.Join(m.ContraptionsDir, "top secret town"))
	want := []string{"top secret town.jaap", "top secret town.json", "top secret town.outline", "top secret town.png"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if inst := m.State.Mods["tw:168"]; inst.Kind != KindContraption {
		t.Fatalf("kind %q", inst.Kind)
	}
	if _, err := os.Stat(m.ModsDir); err == nil {
		t.Fatal("contraption must not create anything in Mods")
	}
	probs, err := m.Verify()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range probs {
		if p.Bad {
			t.Fatalf("verify: %+v", p)
		}
	}
	if err := m.Remove("tw:168"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.ContraptionsDir, "top secret town")); err == nil {
		t.Fatal("remove left the contraption behind")
	}
}

func TestInstallFolderContraptionsAndRefuseExisting(t *testing.T) {
	m := newTestManager(t)
	src := zipOf(t, map[string]string{
		"T-90/T-90.jaap": "x", "T-90/T-90.json": "{}", "T-90/T-90.png": "p", "T-90/T-90.outline": "o",
		"BTP/BTP.jaap": "x", "BTP/BTP.json": "{}",
	})
	os.MkdirAll(filepath.Join(m.ContraptionsDir, "BTP"), 0o755) // the player's own save
	err := m.Install(&Candidate{Key: "gb:1", Name: "Tanks", Path: src})
	if err == nil {
		t.Fatal("overwrote a contraption ppgmods did not install")
	}
	if _, err := os.Stat(filepath.Join(m.ContraptionsDir, "T-90")); err == nil {
		t.Fatal("partial install left behind")
	}
	os.RemoveAll(filepath.Join(m.ContraptionsDir, "BTP"))
	if err := m.Install(&Candidate{Key: "gb:1", Name: "Tanks", Path: src}); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, m.ContraptionsDir); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}
