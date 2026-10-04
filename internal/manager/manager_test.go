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
