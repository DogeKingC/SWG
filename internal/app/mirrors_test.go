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

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"4.0", "3.2", 1}, {"1.75.2", "1.70.8", 1}, {"1.2", "1.10", -1}, {"v2", "1.9.9", 1},
		{"1.0", "1", 0}, {"", "0.1", -1}, {"", "", 0},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSameMod(t *testing.T) {
	if !sameMod("Quick Draw Mod", "51804", "Quick Draw Mod", "51804") {
		t.Error("identical")
	}
	if !sameMod("Science Hazard Minus V:3.1", "Zkryhn", "Science Hazard Minus", "") {
		t.Error("trailing version")
	}
	if sameMod("Quick Draw Mod", "someone", "Quick Draw Mod", "51804") {
		t.Error("different authors matched")
	}
	if sameMod("Melee Pack", "", "Melee Pack 2 Ultimate", "") {
		t.Error("different names matched")
	}
}

func writeVersionedZip(t *testing.T, path, version, ugc string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("Mod/mod.json")
	w.Write([]byte(`{"Name":"Quick Draw Mod","ModVersion":"` + version + `","CreatorUGCIdentity":"` + ugc + `","Scripts":["s.cs"]}`))
	w, _ = zw.Create("Mod/s.cs")
	w.Write([]byte(`class A {}`))
	zw.Close()
	f.Close()
}

// Like Quick Draw: the True Workshop upload is newer by date but carries an
// older ModVersion than the Skymods copy; a copy whose mod.json names a
// different Workshop item is ignored.
func TestFetchWorkshopPicksHighestModVersion(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	const ws = "3801154351"
	dir, _ := CacheDir("sky:" + ws)
	writeVersionedZip(t, filepath.Join(dir, "trueworkshop-153-tw.zip"), "3.2", ws)
	writeVersionedZip(t, filepath.Join(dir, "skymods-478284-sky.zip"), "4.0", ws)
	writeVersionedZip(t, filepath.Join(dir, "topmods-9-other.zip"), "9.9", "1111111111")
	mirrorMemo[ws] = mirrorMemoEntry{at: time.Now(), list: []Mirror{
		{ID: "topmods:9", Source: "top-mods", VersionTime: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), tm: &sources.TMItem{}},
		{ID: "skymods:478284", Source: "Skymods", VersionTime: time.Date(2026, 9, 20, 12, 4, 0, 0, time.UTC), sky: &sources.SkyItem{}},
		{ID: "trueworkshop:153", Source: "True Workshop", Reviewed: true,
			tw: &sources.TWItem{ID: 153, Created: "2026-10-02 07:03:50", Trust: "dev"}},
	}}
	defer delete(mirrorMemo, ws)
	st, _ := manager.LoadState()
	m := &manager.Manager{State: st, ModsDir: t.TempDir()}
	c, err := (&App{Opt: DefaultOptions()}).fetchWorkshop(m, ws)
	if err != nil {
		t.Fatal(err)
	}
	if c.Mirror != "skymods:478284" || c.Version != "4.0" {
		t.Fatalf("picked %s version %s, want skymods:478284 version 4.0", c.Mirror, c.Version)
	}
}

func TestTitleVersion(t *testing.T) {
	for in, want := range map[string]string{
		"Science Hazard Minus V:2.8": "2.8", "Science Hazard Minus V:3.1": "3.1", "AURA MOD BETA 1.2": "1.2",
		"Boneworks Pack: Melee v1.3.0": "1.3.0", "Tiger I 2.0 (German Tank)": "", "Melee Pack 2": "", "Quick Draw Mod": "",
	} {
		if got := TitleVersion(in); got != want {
			t.Errorf("TitleVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
