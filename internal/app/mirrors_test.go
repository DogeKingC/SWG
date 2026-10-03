package app

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/sources"
)

func writeModZip(t *testing.T, path, script string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("Mod/mod.json")
	w.Write([]byte(`{"Name":"Test","Scripts":["script.cs"]}`))
	w, _ = zw.Create("Mod/script.cs")
	w.Write([]byte(script))
	zw.Close()
	f.Close()
}

// The newest mirror copy is infected; ppgmods must fall back to the older
// clean copy instead of refusing or installing the infected one.
func TestFetchWorkshopSkipsInfectedNewestCopy(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	const ws = "1234567890"
	dir, err := CacheDir("sky:" + ws)
	if err != nil {
		t.Fatal(err)
	}
	writeModZip(t, filepath.Join(dir, "topmods-1-new.zip"), `class W { void A() { System.Diagnostics.Process.Start("cmd.exe"); } }`)
	writeModZip(t, filepath.Join(dir, "skymods-2-old.zip"), `class G { void A() { } }`)

	newer := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	older := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mirrorMemo[ws] = mirrorMemoEntry{at: time.Now(), list: []Mirror{
		{ID: "topmods:1", Source: "top-mods", Title: "Test", Version: "01.05.2026", VersionTime: newer, tm: &sources.TMItem{}},
		{ID: "skymods:2", Source: "Skymods", Title: "Test", Version: "2024-01-01", VersionTime: older, sky: &sources.SkyItem{}},
	}}
	defer delete(mirrorMemo, ws)

	st, err := manager.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	m := &manager.Manager{State: st, ModsDir: t.TempDir()}
	a := &App{Opt: DefaultOptions()}

	c, err := a.fetchWorkshop(m, ws)
	if err != nil {
		t.Fatal(err)
	}
	if c.Mirror != "skymods:2" {
		t.Fatalf("picked %s, want the clean older copy skymods:2", c.Mirror)
	}

	// Asking for the infected copy explicitly returns it; the install
	// policy then refuses it (CRITICAL finding).
	a.Opt.Mirror = "topmods:1"
	c, err = a.fetchWorkshop(m, ws)
	if err != nil || c.Mirror != "topmods:1" {
		t.Fatalf("explicit mirror: %v %v", c, err)
	}
	if err := m.Install(c); err == nil {
		t.Fatal("infected copy was installed")
	}
}

func TestFetchWorkshopAllCopiesAfterCutoff(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	const ws = "1234567891"
	after := manager.WormCutoff.Add(24 * time.Hour)
	mirrorMemo[ws] = mirrorMemoEntry{at: time.Now(), list: []Mirror{
		{ID: "topmods:3", Source: "top-mods", VersionTime: after, AfterCutoff: true, tm: &sources.TMItem{}},
	}}
	defer delete(mirrorMemo, ws)
	a := &App{Opt: DefaultOptions()}
	_, err := a.fetchWorkshop(nil, ws)
	var rej *manager.Rejection
	if err == nil || !asRejection(err, &rej) {
		t.Fatalf("want a rejection, got %v", err)
	}
}

func asRejection(err error, r **manager.Rejection) bool {
	x, ok := err.(*manager.Rejection)
	*r = x
	return ok
}
