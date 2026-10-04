package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DogeKingC/SWG/internal/scan"
)

func TestAcceptRisk(t *testing.T) {
	m := &Manager{Policy: Policy{}}
	c := &Candidate{Key: "tw:1"}
	opaque := &scan.Report{Findings: []scan.Finding{{Severity: scan.Critical, Rule: "disguised-binary"}}}
	worm := &scan.Report{Findings: []scan.Finding{{Severity: scan.Critical, Rule: "disguised-binary"}, {Severity: scan.Critical, Rule: "steam-ugc"}}}

	err := m.Check(c, opaque, nil)
	if err == nil || !RiskReason(err.Error()) || !Overridable(err.(*Rejection).Reasons[0]) {
		t.Fatalf("CRITICAL without acceptance: %v", err)
	}
	m.Policy.AcceptRisk = true
	if err := m.Check(c, opaque, nil); err != nil {
		t.Fatalf("accepted risk still refused: %v", err)
	}
	err = m.Check(c, worm, nil)
	if err == nil || RiskReason(err.Error()) || Overridable(err.(*Rejection).Reasons[0]) || !strings.Contains(err.Error(), "steam-ugc") {
		t.Fatalf("worm signature accepted from the window: %v", err)
	}
	m.Policy = Policy{AllowCritical: true}
	if err := m.Check(c, worm, nil); err != nil {
		t.Fatalf("--allow-critical: %v", err)
	}
}

func TestRemoveReportsOtherCopies(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	mods := t.TempDir()
	write := func(folder, json string) {
		os.MkdirAll(filepath.Join(mods, folder), 0o755)
		os.WriteFile(filepath.Join(mods, folder, "mod.json"), []byte(json), 0o644)
	}
	write("Quick Draw [sky-1]", `{"Name":"Quick Draw","Author":"51804","CreatorUGCIdentity":"3801154351"}`)
	write("Quick Draw", `{"Name":"Quick Draw (old)","Author":"x","CreatorUGCIdentity":3801154351}`)
	write("Other", `{"Name":"Other","Author":"y"}`)
	st, _ := LoadState()
	st.Mods["sky:1"] = &Installed{Key: "sky:1", Folders: []string{"Quick Draw [sky-1]"}}
	m := &Manager{State: st, ModsDir: mods}
	others, err := m.RemoveReport("sky:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mods, "Quick Draw [sky-1]")); err == nil {
		t.Fatal("folder not deleted")
	}
	if len(others) != 1 || others[0] != "Quick Draw" {
		t.Fatalf("others = %v", others)
	}
	if st.Mods["sky:1"] != nil {
		t.Fatal("still in state")
	}
}

func TestUGCString(t *testing.T) {
	for in, want := range map[string]string{`"3801154351"`: "3801154351", `3801154351`: "3801154351", `0`: "", `null`: "", `""`: "", `"abc"`: ""} {
		if got := UGCString([]byte(in)); got != want {
			t.Errorf("UGCString(%s) = %q, want %q", in, got, want)
		}
	}
}
