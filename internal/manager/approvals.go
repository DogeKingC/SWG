package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Trlydev/SWG/internal/game"
)

// RE_PPG "Trust and run" approvals (see game.ApprovalDirs). ppgmods can't
// tell which mod an approval belongs to (the fingerprint covers Windows
// paths only the game sees), but it can tell when one appeared: Verify
// remembers the approvals it has seen, and an approval to run a mod
// without security checks that appeared since is flagged with its time, so
// the person can tell whether they clicked it. An approval file RE_PPG
// didn't write (other text) is flagged as forged.

type seenApprovals struct {
	Seen map[string]time.Time `json:"seen"` // file name -> first seen
}

func approvalsPath() (string, error) {
	d, err := ConfigDir()
	return filepath.Join(d, "reppg-approvals.json"), err
}

func loadSeenApprovals() (*seenApprovals, bool) {
	s := &seenApprovals{Seen: map[string]time.Time{}}
	p, err := approvalsPath()
	if err != nil {
		return s, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return s, false
	}
	if json.Unmarshal(b, s) != nil || s.Seen == nil {
		s.Seen = map[string]time.Time{}
	}
	return s, true
}

func (s *seenApprovals) save() error {
	p, err := approvalsPath()
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

// approval is one approval file.
type approval struct {
	path   string
	unsafe bool
	at     time.Time
	forged bool
}

func listApprovals(gameDir string) []approval {
	var out []approval
	for _, dir := range game.ApprovalDirs(gameDir) {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			fp, unsafe := strings.CutSuffix(name, ".unsafe")
			if len(fp) != 64 || strings.Trim(fp, "0123456789abcdef") != "" {
				continue // not an approval
			}
			p := filepath.Join(dir, name)
			info, err := e.Info()
			if err != nil {
				continue
			}
			b, _ := os.ReadFile(p)
			want := game.ApprovalText
			if unsafe {
				want = game.UnsafeApprovalText
			}
			out = append(out, approval{path: p, unsafe: unsafe, at: info.ModTime(), forged: strings.TrimSpace(string(b)) != want})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

// verifyApprovals reports RE_PPG approvals: new "Trust and run" ones (once,
// with their time), forged ones, and how many versions run unchecked.
func (m *Manager) verifyApprovals(gameDir string) []Problem {
	all := listApprovals(gameDir)
	seen, known := loadSeenApprovals()
	if len(all) == 0 {
		if !known {
			seen.save() // the baseline: no approvals, so any later one is new
		}
		return nil
	}
	const label = "RE_PPG approvals"
	var probs []Problem
	unsafe := 0
	for _, a := range all {
		// By file name: the fingerprint names the mod version, wherever
		// RE_PPG's folder is found (a moved library or a config change
		// must not make every approval look new).
		key := filepath.Base(a.path)
		_, wasSeen := seen.Seen[key]
		if a.forged {
			probs = append(probs, Problem{label, fmt.Sprintf("an approval file RE_PPG didn't write (%s, %s): something approved a mod in its place. Revoke the Trust and run approvals in Settings → Game setup, and run Verify again", filepath.Base(a.path), a.at.Format("2006-01-02 15:04")), true, ""})
		} else if a.unsafe && known && !wasSeen {
			probs = append(probs, Problem{label, fmt.Sprintf("a mod version was approved to run WITHOUT RE_PPG's security checks (\"Trust and run\") on %s. If you didn't click Trust and run then, something approved it behind your back: revoke the approvals in Settings → Game setup", a.at.Local().Format("2006-01-02 15:04")), true, ""})
		}
		if a.unsafe {
			unsafe++
		}
		if !wasSeen {
			seen.Seen[key] = time.Now().UTC()
		}
	}
	if unsafe > 0 {
		probs = append(probs, Problem{label, fmt.Sprintf("%d mod version(s) run without RE_PPG's security checks (approved with \"Trust and run\"). Revoke them in Settings → Game setup if you're not sure about one.", unsafe), false, ""})
	}
	// Forget files that are gone.
	present := map[string]bool{}
	for _, a := range all {
		present[filepath.Base(a.path)] = true
	}
	for k := range seen.Seen {
		if !present[k] {
			delete(seen.Seen, k)
		}
	}
	seen.save()
	return probs
}

// RevokeUnsafeApprovals deletes RE_PPG's "Trust and run" approvals (and any
// approval file RE_PPG didn't write): RE_PPG asks again before running
// those mods. It returns how many it deleted.
func (m *Manager) RevokeUnsafeApprovals() (int, error) {
	if m.ModsDir == "" {
		return 0, fmt.Errorf("the game folder is unknown")
	}
	n := 0
	for _, a := range listApprovals(filepath.Dir(m.ModsDir)) {
		if !a.unsafe && !a.forged {
			continue
		}
		if err := os.Remove(a.path); err != nil {
			return n, err
		}
		n++
	}
	m.logf("revoked %d Trust and run approval(s); RE_PPG asks again before running those mods", n)
	return n, nil
}
