package manager

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
