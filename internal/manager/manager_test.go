package manager

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DogeKingC/SWG/internal/loaders"
	"github.com/DogeKingC/SWG/internal/scan"
)

func rejected(err error) bool {
	var r *Rejection
	return errors.As(err, &r)
}

func TestCheckCutoff(t *testing.T) {
	m := &Manager{}
	clean := &scan.Report{}
	before := &Candidate{SteamOrig: true, Revision: WormCutoff.Add(-time.Hour)}
	after := &Candidate{SteamOrig: true, Revision: WormCutoff.Add(time.Hour)}
	unknown := &Candidate{SteamOrig: true}
	if err := m.Check(before, clean, nil); err != nil {
		t.Fatalf("pre-cutoff copy refused: %v", err)
	}
	if !rejected(m.Check(after, clean, nil)) || !rejected(m.Check(unknown, clean, nil)) {
		t.Fatal("post-cutoff or undated Steam copy accepted")
	}
	m.Policy.AllowAfterCutoff = true
	if err := m.Check(after, clean, nil); err != nil {
		t.Fatalf("override ignored: %v", err)
	}
}

func TestCheckCooldownAndSeverity(t *testing.T) {
	m := &Manager{Policy: Policy{Cooldown: 48 * time.Hour}}
	fresh := &Candidate{Revision: time.Now().Add(-time.Hour)}
	if !rejected(m.Check(fresh, &scan.Report{}, nil)) {
		t.Fatal("file inside cooldown accepted")
	}
	old := &Candidate{Revision: time.Now().Add(-72 * time.Hour)}
	high := &scan.Report{Findings: []scan.Finding{{Severity: scan.High, Rule: "base64", File: "a.cs"}}}
	crit := &scan.Report{Findings: []scan.Finding{{Severity: scan.Critical, Rule: "network", File: "a.cs"}}}
	if !rejected(m.Check(old, high, nil)) || !rejected(m.Check(old, crit, nil)) {
		t.Fatal("HIGH/CRITICAL accepted by default")
	}
	m.Policy.AllowHigh = true
	if m.Check(old, high, nil) != nil || !rejected(m.Check(old, crit, nil)) {
		t.Fatal("--allow-high must allow HIGH but not CRITICAL")
	}
}

func TestCheckUpdateNewFindings(t *testing.T) {
	m := &Manager{Policy: Policy{AllowHigh: true}}
	prev := &Installed{Findings: []string{"base64|Old Name [gb-1]/a.cs"}}
	same := &scan.Report{Findings: []scan.Finding{{Severity: scan.High, Rule: "base64", File: "New Name [gb-1]/a.cs"}}}
	if err := m.Check(&Candidate{}, same, prev); err != nil {
		t.Fatalf("unchanged finding after folder rename treated as new: %v", err)
	}
	added := &scan.Report{Findings: append(same.Findings, scan.Finding{Severity: scan.Medium, Rule: "file-write", File: "x/b.cs"})}
	if !rejected(m.Check(&Candidate{}, added, prev)) {
		t.Fatal("update adding a finding was accepted")
	}
}

func TestBlocklistCannotBeOverridden(t *testing.T) {
	m := &Manager{Policy: Policy{AllowHigh: true, AllowCritical: true, AllowAfterCutoff: true}}
	rep := &scan.Report{Findings: []scan.Finding{{Severity: scan.Critical, Rule: "blocklisted", Detail: "blocklisted: worm"}}}
	if !rejected(m.Check(&Candidate{}, rep, nil)) {
		t.Fatal("blocklisted candidate accepted")
	}
}

func TestVerifyFindsWormInGameCode(t *testing.T) {
	game := t.TempDir()
	mods := filepath.Join(game, "Mods")
	os.MkdirAll(mods, 0o755)
	cm := filepath.Join(game, "CompiledMods")
	os.MkdirAll(cm, 0o755)
	os.WriteFile(filepath.Join(cm, "Amy-Atomics-1.dll"), []byte("MZ ... get_NewCommunityFile WhereUserPublished"), 0o644)
	os.WriteFile(filepath.Join(cm, "Clean-2.dll"), []byte("MZ ... nothing"), 0o644)
	m := &Manager{ModsDir: mods, State: &State{Mods: map[string]*Installed{}}}
	probs, err := m.Verify()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range probs {
		if strings.HasPrefix(p.Issue, "WORM") {
			n++
			if !p.Bad || !strings.Contains(p.Folder, "Amy-Atomics-1.dll") {
				t.Errorf("unexpected %+v", p)
			}
		}
	}
	if n != 1 {
		t.Errorf("want 1 worm problem, got %+v", probs)
	}
}

func TestVerifyChecksBepInEx(t *testing.T) {
	g := t.TempDir()
	mods := filepath.Join(g, "Mods")
	os.MkdirAll(mods, 0o755)
	pl := filepath.Join(g, "BepInEx", "plugins")
	os.MkdirAll(filepath.Join(pl, "RE_PPG"), 0o755)
	os.WriteFile(filepath.Join(pl, "RE_PPG", "RE_PPG.Runtime.dll"), []byte("MZ clean"), 0o644)
	os.WriteFile(filepath.Join(pl, "Helper.dll"), []byte("MZ clean"), 0o644)
	os.WriteFile(filepath.Join(pl, "Evil.dll"), []byte("MZ ... FPSPlusPlus.entry ... api.ipify.org"), 0o644)
	// A loader that legitimately touches the same APIs as the worm.
	os.WriteFile(filepath.Join(pl, "RE_PPG", "RE_PPG.Guard.dll"), []byte("MZ RejectShadyCode BinaryFormatter"), 0o644)
	m := &Manager{ModsDir: mods, State: &State{Mods: map[string]*Installed{}}}
	probs, err := m.Verify()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Problem{}
	for _, p := range probs {
		got[p.Folder] = p
	}
	if p := got["BepInEx/plugins/Evil.dll"]; !p.Bad || !strings.HasPrefix(p.Issue, "WORM") {
		t.Errorf("Evil.dll: %+v", p)
	}
	if p, ok := got["BepInEx/plugins/Helper.dll"]; !ok || p.Bad {
		t.Errorf("Helper.dll should be listed, not bad: %+v", p)
	}
	if _, ok := got["BepInEx/plugins/RE_PPG/RE_PPG.Runtime.dll"]; ok {
		t.Errorf("RE_PPG's own plugin listed")
	}
}

// The game's own code defines the APIs the worm used; it must not be
// reported (it was, in v0.1.30-31).
func TestVerifyGameManagedNoFalsePositives(t *testing.T) {
	g := t.TempDir()
	mods := filepath.Join(g, "Mods")
	os.MkdirAll(mods, 0o755)
	man := filepath.Join(g, "People Playground_Data", "Managed")
	os.MkdirAll(man, 0o755)
	os.WriteFile(filepath.Join(man, "mscorlib.dll"), []byte("MZ BinaryFormatter DelegateSerializationHolder"), 0o644)
	os.WriteFile(filepath.Join(man, "Assembly-CSharp.dll"), []byte("MZ RejectShadyCode NewCommunityFile"), 0o644)
	os.WriteFile(filepath.Join(man, "Facepunch.Steamworks.Win64.dll"), []byte("MZ NewCommunityFile WhereUserPublished"), 0o644)
	os.WriteFile(filepath.Join(man, "UnityEngine.CoreModule.dll"), []byte("MZ m_PersistentCalls m_TargetAssemblyTypeName"), 0o644)
	os.WriteFile(filepath.Join(man, "Xq7Kw.dll"), []byte("MZ ... FPSPlusPlus ... STEAM_CONFIG"), 0o644)
	m := &Manager{ModsDir: mods, State: &State{Mods: map[string]*Installed{}}}
	probs, err := m.Verify()
	if err != nil {
		t.Fatal(err)
	}
	var worm []string
	for _, p := range probs {
		if strings.HasPrefix(p.Issue, "WORM") {
			worm = append(worm, p.Folder)
		}
	}
	if len(worm) != 1 || !strings.HasSuffix(worm[0], "Xq7Kw.dll") {
		t.Errorf("want only Xq7Kw.dll, got %v", worm)
	}
}

func TestVerifyLoaderFilesAgainstReleases(t *testing.T) {
	g := t.TempDir()
	mods := filepath.Join(g, "Mods")
	os.MkdirAll(mods, 0o755)
	os.MkdirAll(filepath.Join(g, "BepInEx", "core"), 0o755)
	os.MkdirAll(filepath.Join(g, "RE_PPG"), 0o755)
	write := func(rel, body string, age time.Duration) {
		p := filepath.Join(g, rel)
		os.WriteFile(p, []byte(body), 0o644)
		os.Chtimes(p, time.Now().Add(-age), time.Now().Add(-age))
	}
	write("BepInEx/core/BepInEx.dll", "official core", 72*time.Hour)
	write("RE_PPG/RE_PPG.Compiler.exe", "official compiler", 72*time.Hour)
	write("RE_PPG/Replaced.dll", "not from any release", 72*time.Hour)
	write("RE_PPG/JustUpdated.dll", "brand new", time.Minute)
	ix := &loaders.Index{Updated: time.Now().Add(-time.Hour)}
	for _, s := range []string{"official core", "official compiler"} {
		h, _ := fileSHAOf([]byte(s))
		ix.Add(h, loaders.File{Project: "RE_PPG", Version: "v0.2.16"})
	}
	m := &Manager{ModsDir: mods, State: &State{Mods: map[string]*Installed{}}, Releases: func() *loaders.Index { return ix }}
	probs, err := m.Verify()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Problem{}
	for _, p := range probs {
		got[p.Folder] = p
	}
	if p := got["RE_PPG/Replaced.dll"]; !p.Bad {
		t.Errorf("Replaced.dll should be bad: %+v", p)
	}
	if p, ok := got["RE_PPG/JustUpdated.dll"]; !ok || p.Bad {
		t.Errorf("JustUpdated.dll should be a note: %+v", p)
	}
	if p := got["RE_PPG / BepInEx"]; !strings.Contains(p.Issue, "2 files match official releases") {
		t.Errorf("summary: %+v", p)
	}
}

func fileSHAOf(b []byte) (string, error) {
	f, err := os.CreateTemp("", "sha")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	f.Write(b)
	f.Close()
	return fileSHA(f.Name())
}

// The game rewrites mod.json (its Active switch); names that aren't valid
// UTF-8 are stored with U+FFFD. Neither is tampering.
func TestVerifyIgnoresGameRewritesAndNameEncoding(t *testing.T) {
	g := t.TempDir()
	mods := filepath.Join(g, "Mods")
	dir := filepath.Join(mods, "Plane")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "mod.json"), []byte(`{"Name":"Plane","Scripts":["script.cs"],"Active":false}`), 0o644)
	os.WriteFile(filepath.Join(dir, "script.cs"), []byte("class A {}"), 0o644)
	os.WriteFile(filepath.Join(dir, "MIDDLE\xff.png"), []byte("png"), 0o644)
	files := map[string]string{}
	if err := hashTree(dir, "Plane", files); err != nil {
		t.Fatal(err)
	}
	// What state.json gives back: the invalid name replaced.
	recorded := map[string]string{}
	for k, v := range files {
		recorded[strings.ToValidUTF8(k, "�")] = v
	}
	inst := &Installed{Key: "sky:1", Name: "Plane", Folders: []string{"Plane"}, Files: recorded}
	m := &Manager{ModsDir: mods, State: &State{Mods: map[string]*Installed{"sky:1": inst}}}
	// The game turns the mod on.
	os.WriteFile(filepath.Join(dir, "mod.json"), []byte(`{"Name":"Plane","Scripts":["script.cs"],"Active":true}`), 0o644)
	probs, _ := m.Verify()
	for _, p := range probs {
		if p.Bad {
			t.Errorf("unexpected problem %+v", p)
		}
	}
	// An injection: a new script listed in mod.json.
	os.WriteFile(filepath.Join(dir, "evil.cs"), []byte("class B {}"), 0o644)
	os.WriteFile(filepath.Join(dir, "mod.json"), []byte(`{"Name":"Plane","Scripts":["script.cs","evil.cs"],"Active":true}`), 0o644)
	probs, _ = m.Verify()
	bad := 0
	for _, p := range probs {
		if p.Bad {
			bad++
		}
	}
	if bad != 2 {
		t.Errorf("want mod.json changed + evil.cs new, got %+v", probs)
	}
}

// A blocked Workshop item stays blocked when it comes from another site or
// a local file: by alias, and by the Workshop ID in its mod.json.
func TestBlocklistMatchesMirroredCopies(t *testing.T) {
	bl := &Blocklist{Entries: []Entry{{WorkshopID: "3603531358", Reason: "worm upload"}}, sha: map[string]Entry{}}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "m"), 0o755)
	os.WriteFile(filepath.Join(dir, "m", "mod.json"), []byte(`{"Name":"x","CreatorUGCIdentity":3603531358}`), 0o644)
	for name, c := range map[string]*Candidate{
		"True Workshop copy": {Key: "tw:12"},
		"local file":         {Key: "local:abcdef"},
		"alias":              {Key: "ow:x", Aliases: []string{"sky:3603531358"}},
	} {
		rep := &scan.Report{}
		bl.Check(c, dir, rep)
		if m := (&Manager{}); !rejected(m.Check(c, rep, nil)) {
			t.Errorf("%s of a blocklisted Workshop item was not blocked: %+v", name, rep.Findings)
		}
	}
	rep := &scan.Report{}
	bl.Check(&Candidate{Key: "tw:13"}, t.TempDir(), rep)
	if len(rep.Findings) != 0 {
		t.Errorf("unrelated mod blocked: %+v", rep.Findings)
	}
}

// A folder that appeared in Mods/ is adopted automatically while the window
// is open. Adopting must not silence what Verify reports about it.
func TestVerifyStillFlagsAdoptedDangerousMod(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	game := t.TempDir()
	mods := filepath.Join(game, "Mods")
	evil := filepath.Join(mods, "Cool Guns")
	os.MkdirAll(evil, 0o755)
	os.WriteFile(filepath.Join(evil, "mod.json"), []byte(`{"Name":"Cool Guns","Scripts":["script.cs"]}`), 0o644)
	os.WriteFile(filepath.Join(evil, "script.cs"), []byte(`class M { void Main() { System.Diagnostics.Process.Start("calc"); } }`), 0o644)
	st, _ := LoadState()
	m := &Manager{ModsDir: mods, State: st}
	bad := func() bool {
		probs, err := m.Verify()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range probs {
			if p.Bad && strings.HasPrefix(p.Folder, "Cool Guns") {
				return true
			}
		}
		return false
	}
	if !bad() {
		t.Fatal("unmanaged dangerous mod not flagged")
	}
	if _, err := m.Adopt("local:m-cool-guns", "Cool Guns", "", evil, "", KindMod, "Cool Guns"); err != nil {
		t.Fatal(err)
	}
	if !bad() {
		t.Fatal("adopting the folder hid its CRITICAL findings from Verify")
	}
}
