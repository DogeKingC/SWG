package manager

import (
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
