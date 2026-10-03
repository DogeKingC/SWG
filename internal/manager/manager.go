package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/DogeKingC/SWG/internal/archive"
	"github.com/DogeKingC/SWG/internal/scan"
)

// WormCutoff is the start of the September 2026 Workshop worm. Steam-origin
// copies revised on or after this moment are refused by default: Valve
// deleted everything uploaded or updated since then because the worm was
// inserting itself into existing mods.
var WormCutoff = time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)

// FebruaryWindow brackets the February 2026 Workshop incident (Workshop
// disabled Feb 1, patched Feb 2). The bounds are approximate, so copies in it
// are flagged for review rather than refused.
var FebruaryWindow = [2]time.Time{
	time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC),
	time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC),
}

type Policy struct {
	AllowHigh        bool          // install despite HIGH findings
	AllowCritical    bool          // install despite CRITICAL findings
	AllowAfterCutoff bool          // accept Steam-origin copies revised after WormCutoff
	Cooldown         time.Duration // refuse source files younger than this
	AllowNewFindings bool          // updates: accept findings the installed version did not have
	DryRun           bool
}

type Manager struct {
	ModsDir   string
	State     *State
	Policy    Policy
	Blocklist *Blocklist
	Log       func(format string, a ...any)
}

// Candidate is a downloaded archive or folder waiting to be installed.
type Candidate struct {
	Key        string
	Name       string
	Source     string
	Path       string // archive file or folder
	FileID     int
	Version    string
	Revision   time.Time
	SteamOrig  bool // came from the Steam Workshop (mirror, cache or backup)
	ArchiveSHA string
}

type Rejection struct{ Reasons []string }

func (r *Rejection) Error() string { return "refused: " + strings.Join(r.Reasons, "; ") }

func (m *Manager) logf(format string, a ...any) {
	if m.Log != nil {
		m.Log(format, a...)
	}
}

func (m *Manager) workDir(name string) (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	return p, os.MkdirAll(p, 0o755)
}

// Stage unpacks (or copies) the candidate into a fresh staging folder, strips
// build output and scans it. The caller removes the returned folder.
func (m *Manager) Stage(c *Candidate) (string, *scan.Report, error) {
	work, err := m.workDir("staging")
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(work, "stage-")
	if err != nil {
		return "", nil, err
	}
	st, err := os.Stat(c.Path)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	if st.IsDir() {
		err = copyTree(c.Path, filepath.Join(dir, filepath.Base(c.Path)))
	} else {
		err = archive.Extract(c.Path, dir)
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	if removed, err := scan.PruneBuildOutput(dir); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	} else if len(removed) > 0 {
		m.logf("  stripped build output: %s", strings.Join(removed, ", "))
	}
	rep, err := scan.Dir(dir)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	m.Blocklist.Check(c, dir, rep)
	return dir, rep, nil
}

// Check applies the policy to a staged candidate. prev is the installed
// version for updates (nil for a fresh install).
func (m *Manager) Check(c *Candidate, rep *scan.Report, prev *Installed) error {
	var reasons []string
	p := m.Policy
	if c.SteamOrig && !c.Revision.IsZero() && !c.Revision.Before(WormCutoff) && !p.AllowAfterCutoff {
		reasons = append(reasons, fmt.Sprintf("Steam copy revised %s, on/after the worm cutoff %s (override: --allow-after-cutoff)",
			c.Revision.Format("2006-01-02 15:04"), WormCutoff.Format("2006-01-02")))
	}
	if c.SteamOrig && c.Revision.IsZero() && !p.AllowAfterCutoff {
		reasons = append(reasons, "revision date unknown, cannot prove it predates the worm (override: --allow-after-cutoff)")
	}
	if !c.SteamOrig && p.Cooldown > 0 && !c.Revision.IsZero() && time.Since(c.Revision) < p.Cooldown {
		reasons = append(reasons, fmt.Sprintf("file uploaded %s ago, cooldown is %s (override: --cooldown 0)",
			time.Since(c.Revision).Round(time.Minute), p.Cooldown))
	}
	switch max := rep.Max(); {
	case max >= scan.Critical && !p.AllowCritical:
		reasons = append(reasons, "CRITICAL scan findings (override: --allow-critical, only if you have read the code)")
	case max >= scan.High && !p.AllowHigh && !p.AllowCritical:
		reasons = append(reasons, "HIGH scan findings (override: --allow-high after reviewing them)")
	}
	if prev != nil && !p.AllowNewFindings {
		old := map[string]bool{}
		for _, k := range prev.Findings {
			old[stripFolder(k)] = true
		}
		var added []string
		for k := range rep.Keys() {
			if !old[stripFolder(k)] {
				added = append(added, k)
			}
		}
		if len(added) > 0 {
			reasons = append(reasons, "update adds new findings not present in the installed version: "+strings.Join(added, ", ")+" (override: --allow-new-findings)")
		}
	}
	for _, f := range rep.Findings {
		if f.Rule == "blocklisted" {
			reasons = append(reasons, f.Detail+" (blocklist cannot be overridden)")
		}
	}
	if len(reasons) > 0 {
		return &Rejection{reasons}
	}
	return nil
}

// stripFolder drops the top-level folder from "rule|folder/file" so a mod
// whose archive folder was renamed between versions still compares equal.
func stripFolder(k string) string {
	rule, file, ok := strings.Cut(k, "|")
	if !ok {
		return k
	}
	if _, rest, ok := strings.Cut(file, "/"); ok {
		file = rest
	}
	return rule + "|" + file
}

// modRoots returns folders under dir that contain a mod.json.
func modRoots(dir string) ([]string, error) {
	var roots []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), "mod.json") {
			roots = append(roots, filepath.Dir(p))
			return filepath.SkipDir
		}
		return nil
	})
	return roots, err
}

var reUnsafeName = regexp.MustCompile(`[^\w\-. ()\[\]]+`)

func folderName(root, key string) string {
	name := filepath.Base(root)
	if b, err := os.ReadFile(filepath.Join(root, "mod.json")); err == nil {
		var mj struct{ Name string }
		if json.Unmarshal(trimBOM(b), &mj) == nil && strings.TrimSpace(mj.Name) != "" {
			name = mj.Name
		}
	}
	name = strings.TrimSpace(reUnsafeName.ReplaceAllString(name, "_"))
	if len(name) > 60 {
		name = name[:60]
	}
	tag := strings.ReplaceAll(key, ":", "-")
	if len(tag) > 24 {
		tag = tag[:24]
	}
	return fmt.Sprintf("%s [%s]", name, tag)
}

func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// Install stages, checks and installs a candidate, replacing the previous
// version (kept as a backup for rollback).
func (m *Manager) Install(c *Candidate) error {
	if m.ModsDir == "" {
		return errors.New("Mods folder unknown")
	}
	m.logf("scanning %s", c.Name)
	dir, rep, err := m.Stage(c)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	PrintReport(m.logf, rep, dir)

	prev := m.State.Mods[c.Key]
	if err := m.Check(c, rep, prev); err != nil {
		return err
	}
	roots, err := modRoots(dir)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return &Rejection{[]string{"no mod.json found; this is not a C# mod (contraptions and skins go in other folders)"}}
	}
	if m.Policy.DryRun {
		m.logf("dry run: would install %d mod folder(s) for %s", len(roots), c.Key)
		return nil
	}

	inst := &Installed{
		Key: c.Key, Name: c.Name, Source: c.Source, FileID: c.FileID, Version: c.Version,
		Revision: c.Revision, ArchiveSHA: c.ArchiveSHA, Files: map[string]string{}, InstalledAt: time.Now().UTC(),
	}
	if prev != nil {
		inst.Pinned = prev.Pinned
	}
	for k := range rep.Keys() {
		inst.Findings = append(inst.Findings, k)
	}
	used := map[string]bool{}
	for _, root := range roots {
		name := folderName(root, c.Key)
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%s %d", folderName(root, c.Key), i)
		}
		used[name] = true
		if owner := m.State.OwnerOf(name); owner != nil && owner.Key != c.Key {
			return fmt.Errorf("folder %q belongs to %s", name, owner.Key)
		}
		inst.Folders = append(inst.Folders, name)
	}
	if prev != nil {
		if err := m.backup(prev); err != nil {
			return fmt.Errorf("backing up previous version: %w", err)
		}
	}
	if placed, err := m.place(roots, inst); err != nil {
		for _, f := range inst.Folders[:placed] {
			os.RemoveAll(filepath.Join(m.ModsDir, f))
		}
		if prev != nil {
			if rerr := m.Rollback(c.Key); rerr != nil {
				return fmt.Errorf("%v (and restoring the previous version failed: %v)", err, rerr)
			}
			m.State.Mods[c.Key].Pinned = prev.Pinned
			m.State.Save()
		}
		return err
	}
	m.State.Mods[c.Key] = inst
	m.logf("installed %s -> %s", c.Key, strings.Join(inst.Folders, ", "))
	return m.State.Save()
}

// place moves staged mod roots into Mods/ and returns how many it placed.
func (m *Manager) place(roots []string, inst *Installed) (int, error) {
	for i, root := range roots {
		target := filepath.Join(m.ModsDir, inst.Folders[i])
		if _, err := os.Stat(target); err == nil {
			if m.State.OwnerOf(inst.Folders[i]) == nil {
				return i, fmt.Errorf("%s already exists and was not installed by ppgmods; move it away first", target)
			}
			if err := os.RemoveAll(target); err != nil {
				return i, err
			}
		}
		if err := os.MkdirAll(m.ModsDir, 0o755); err != nil {
			return i, err
		}
		if err := moveTree(root, target); err != nil {
			return i + 1, err
		}
		if err := hashTree(target, inst.Folders[i], inst.Files); err != nil {
			return i + 1, err
		}
	}
	return len(roots), nil
}

// backup moves an installed version's folders into the backups area and
// removes them from Mods/.
func (m *Manager) backup(prev *Installed) error {
	base, err := m.workDir("backups")
	if err != nil {
		return err
	}
	dest := filepath.Join(base, strings.ReplaceAll(prev.Key, ":", "-"), time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, f := range prev.Folders {
		src := filepath.Join(m.ModsDir, f)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := moveTree(src, filepath.Join(dest, f)); err != nil {
			return err
		}
	}
	b, _ := json.MarshalIndent(prev, "", "  ")
	return os.WriteFile(filepath.Join(dest, "entry.json"), b, 0o644)
}

// Rollback restores the most recent backup of key.
func (m *Manager) Rollback(key string) error {
	base, err := m.workDir("backups")
	if err != nil {
		return err
	}
	dir := filepath.Join(base, strings.ReplaceAll(key, ":", "-"))
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		return fmt.Errorf("no backup for %s", key)
	}
	latest := filepath.Join(dir, ents[len(ents)-1].Name())
	b, err := os.ReadFile(filepath.Join(latest, "entry.json"))
	if err != nil {
		return err
	}
	var prev Installed
	if err := json.Unmarshal(b, &prev); err != nil {
		return err
	}
	if cur := m.State.Mods[key]; cur != nil {
		for _, f := range cur.Folders {
			if err := os.RemoveAll(filepath.Join(m.ModsDir, f)); err != nil {
				return err
			}
		}
	}
	for _, f := range prev.Folders {
		if err := moveTree(filepath.Join(latest, f), filepath.Join(m.ModsDir, f)); err != nil {
			return err
		}
	}
	prev.Pinned = true // stop the next update from re-applying the bad version
	m.State.Mods[key] = &prev
	if err := os.RemoveAll(latest); err != nil {
		return err
	}
	m.logf("rolled back %s to version installed %s (now pinned; `unpin` to resume updates)", key, prev.InstalledAt.Format("2006-01-02"))
	return m.State.Save()
}

func (m *Manager) Remove(key string) error {
	inst := m.State.Mods[key]
	if inst == nil {
		return fmt.Errorf("%s is not installed", key)
	}
	for _, f := range inst.Folders {
		if err := os.RemoveAll(filepath.Join(m.ModsDir, f)); err != nil {
			return err
		}
	}
	delete(m.State.Mods, key)
	m.logf("removed %s", key)
	return m.State.Save()
}

// Problem is a verify result for one Mods/ folder.
type Problem struct {
	Folder string
	Issue  string
}

// Verify re-hashes installed mods to detect tampering (the September worm
// inserted itself into other mods' files) and scans folders ppgmods did not
// install.
func (m *Manager) Verify() ([]Problem, error) {
	var probs []Problem
	for _, inst := range m.State.Sorted() {
		now := map[string]string{}
		for _, f := range inst.Folders {
			if _, err := os.Stat(filepath.Join(m.ModsDir, f)); err != nil {
				probs = append(probs, Problem{f, "missing"})
				continue
			}
			if err := hashTree(filepath.Join(m.ModsDir, f), f, now); err != nil {
				return nil, err
			}
		}
		for p, h := range inst.Files {
			if g, ok := now[p]; !ok {
				probs = append(probs, Problem{p, "file deleted since install"})
			} else if g != h {
				probs = append(probs, Problem{p, "file CHANGED since install (possible tampering)"})
			}
		}
		for p := range now {
			if _, ok := inst.Files[p]; !ok {
				probs = append(probs, Problem{p, "NEW file appeared since install (possible injection)"})
			}
		}
	}
	ents, err := os.ReadDir(m.ModsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range ents {
		if !e.IsDir() || m.State.OwnerOf(e.Name()) != nil {
			continue
		}
		rep, err := scan.Dir(filepath.Join(m.ModsDir, e.Name()))
		if err != nil {
			return nil, err
		}
		issue := "not managed by ppgmods; scan clean"
		if rep.Max() >= scan.Medium {
			issue = fmt.Sprintf("not managed by ppgmods; scan max %s (%d findings) - run `ppgmods scan` on it", rep.Max(), len(rep.Findings))
		}
		probs = append(probs, Problem{e.Name(), issue})
	}
	return probs, nil
}

// PrintReport prints findings at MEDIUM and above, grouped by rule.
func PrintReport(logf func(string, ...any), rep *scan.Report, root string) {
	logf("  %d files, %d C# scripts, highest finding: %s", rep.Files, rep.Scripts, maxName(rep))
	groups := map[string][]scan.Finding{}
	var order []string
	for _, f := range rep.Findings {
		if f.Severity < scan.Medium {
			continue
		}
		k := f.Severity.String() + " " + f.Rule
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], f)
	}
	for _, k := range order {
		fs := groups[k]
		f := fs[0]
		loc := filepath.ToSlash(f.File)
		if f.Line > 0 {
			loc += fmt.Sprintf(":%d", f.Line)
		}
		more := ""
		if len(fs) > 1 {
			more = fmt.Sprintf(" (+%d more)", len(fs)-1)
		}
		logf("  [%s] %s: %s%s", k, loc, f.Detail, more)
	}
}

func maxName(rep *scan.Report) string {
	if rep.Max() < 0 {
		return "none"
	}
	return rep.Max().String()
}
