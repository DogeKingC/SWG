package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/DogeKingC/SWG/internal/archive"
	"github.com/DogeKingC/SWG/internal/game"
	"github.com/DogeKingC/SWG/internal/loaders"
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
	AllowCritical    bool          // install despite CRITICAL findings (command line)
	AcceptRisk       bool          // install despite CRITICAL findings, except the worm's signatures (window)
	AllowAfterCutoff bool          // accept Steam-origin copies revised after WormCutoff
	Cooldown         time.Duration // refuse source files younger than this
	AllowNewFindings bool          // updates: accept findings the installed version did not have
	DryRun           bool
}

type Manager struct {
	ModsDir         string
	ContraptionsDir string
	State           *State
	Policy          Policy
	Blocklist       *Blocklist
	Releases        func() *loaders.Index // official RE_PPG/BepInEx files (nil: offline)
	Log             func(format string, a ...any)
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
	SteamOrig  bool     // came from the Steam Workshop (mirror, cache or backup)
	Mirror     string   // which mirror copy, e.g. topmods:4482
	Reviewed   bool     // a person at the source reviewed it (True Workshop "dev" trust)
	Aliases    []string // other refs for the same mod
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
	rep, err := scan.DirWith(dir, m.scanOpts())
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
	// The cooldown gives the community time to catch a bad upload; uploads a
	// source's maintainers already reviewed skip it.
	if !c.SteamOrig && !c.Reviewed && p.Cooldown > 0 && !c.Revision.IsZero() && time.Since(c.Revision) < p.Cooldown {
		reasons = append(reasons, fmt.Sprintf("file uploaded %s ago, cooldown is %s (override: --cooldown 0)",
			time.Since(c.Revision).Round(time.Minute), p.Cooldown))
	}
	switch max := rep.Max(); {
	case max >= scan.Critical && !p.AllowCritical:
		if worm := wormFindings(rep); len(worm) > 0 {
			reasons = append(reasons, "CRITICAL findings match what the worm did ("+strings.Join(worm, ", ")+") (override: --allow-critical, command line only, only if you have read the code)")
		} else if !p.AcceptRisk {
			reasons = append(reasons, "CRITICAL scan findings (override: accept the risk after reading the findings, or --allow-critical)")
		}
	case max >= scan.High && !p.AllowHigh && !p.AllowCritical && !p.AcceptRisk:
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
	if name = strings.TrimRight(name, ". "); name == "" {
		name = "mod"
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
	kind := KindMod
	roots, err := modRoots(dir)
	if err != nil {
		return err
	}
	var names []string
	if len(roots) == 0 {
		if roots, names, err = contraptionRoots(dir); err != nil {
			return err
		}
		kind = KindContraption
	}
	if len(roots) == 0 {
		return &Rejection{[]string{"found neither a mod (mod.json) nor a contraption (.jaap); skins and other content are not supported"}}
	}
	if kind == KindContraption && m.ContraptionsDir == "" {
		return errors.New("Contraptions folder unknown")
	}
	if m.Policy.DryRun {
		m.logf("dry run: would install %d %s folder(s) for %s", len(roots), kind, c.Key)
		return nil
	}

	inst := &Installed{
		Key: c.Key, Name: c.Name, Source: c.Source, FileID: c.FileID, Version: c.Version, Mirror: c.Mirror,
		Revision: c.Revision, ArchiveSHA: c.ArchiveSHA, Files: map[string]string{}, InstalledAt: time.Now().UTC(), Kind: kind,
		ScanMax:      maxName(rep),
		RiskAccepted: rep.Max() >= scan.Critical && m.Policy.AcceptRisk && !m.Policy.AllowCritical,
	}
	if prev != nil {
		inst.Pinned = prev.Pinned
	}
	for k := range rep.Keys() {
		inst.Findings = append(inst.Findings, k)
	}
	used := map[string]bool{}
	for i, root := range roots {
		var name string
		if kind == KindContraption {
			// The game expects Contraptions/<name>/<name>.jaap.
			name = names[i]
			if used[name] {
				continue
			}
		} else {
			name = folderName(root, c.Key)
			for i := 2; used[name]; i++ {
				name = fmt.Sprintf("%s %d", folderName(root, c.Key), i)
			}
		}
		used[name] = true
		if owner := m.State.OwnerOf(kind, name); owner != nil && owner.Key != c.Key {
			return fmt.Errorf("%s folder %q belongs to %s", kind, name, owner.Key)
		}
		inst.Folders = append(inst.Folders, name)
	}
	roots = roots[:len(inst.Folders)]
	if prev != nil {
		if err := m.backup(prev); err != nil {
			return fmt.Errorf("backing up previous version: %w", err)
		}
	}
	if placed, err := m.place(roots, inst); err != nil {
		for _, f := range inst.Folders[:placed] {
			removeFolder(m.dirFor(inst), f)
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
	name, author := modJSONNames(filepath.Join(m.dirFor(inst), inst.Folders[0]))
	inst.Author = author
	// An import is named after its file or folder; the mod's own name is better.
	if base := filepath.Base(c.Path); name != "" && (inst.Name == "" || inst.Name == base || inst.Name == strings.TrimSuffix(base, filepath.Ext(base))) {
		inst.Name = name
	}
	if ugc := workshopIDIn(filepath.Join(m.dirFor(inst), inst.Folders[0])); ugc != "" && c.Key != "sky:"+ugc {
		inst.Aliases = append(inst.Aliases, "sky:"+ugc) // shows as installed on its Workshop card too
	}
	inst.Aliases = append(inst.Aliases, c.Aliases...)
	seen := map[string]bool{c.Key: true}
	var aliases []string
	for _, a := range inst.Aliases {
		if !seen[a] {
			seen[a] = true
			aliases = append(aliases, a)
		}
	}
	inst.Aliases = aliases
	m.State.Mods[c.Key] = inst
	m.logf("installed %s -> %s", c.Key, strings.Join(inst.Folders, ", "))
	return m.State.Save()
}

// place moves staged roots into Mods/ or Contraptions/ and returns how
// many it placed.
func (m *Manager) place(roots []string, inst *Installed) (int, error) {
	base := m.dirFor(inst)
	for i, root := range roots {
		target := filepath.Join(base, inst.Folders[i])
		if _, err := os.Stat(target); err == nil {
			if m.State.OwnerOf(inst.Kind, inst.Folders[i]) == nil {
				return i, fmt.Errorf("%s already exists and was not installed by ppgmods; move it away first", target)
			}
			if err := os.RemoveAll(target); err != nil {
				return i, err
			}
		}
		if err := os.MkdirAll(base, 0o755); err != nil {
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
		src := filepath.Join(m.dirFor(prev), f)
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
			if err := removeFolder(m.dirFor(cur), f); err != nil {
				return err
			}
		}
	}
	for _, f := range prev.Folders {
		if err := moveTree(filepath.Join(latest, f), filepath.Join(m.dirFor(&prev), f)); err != nil {
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

func (m *Manager) SetPinned(key string, pinned bool) error {
	inst := m.State.Mods[key]
	if inst == nil {
		return fmt.Errorf("%s is not installed", key)
	}
	inst.Pinned = pinned
	return m.State.Save()
}

func (m *Manager) Remove(key string) error {
	_, err := m.RemoveReport(key)
	return err
}

// RemoveReport removes an installed item and returns the other folders
// that still hold a copy of the same mod (same Workshop ID in mod.json, or
// same name and author): ppgmods didn't install those, so it leaves them,
// but the game still loads them.
func (m *Manager) RemoveReport(key string) ([]string, error) {
	inst := m.State.Mods[key]
	if inst == nil {
		return nil, fmt.Errorf("%s is not installed", key)
	}
	base := m.dirFor(inst)
	var idents []modIdent
	for _, f := range inst.Folders {
		idents = append(idents, readIdent(filepath.Join(base, f)))
	}
	for _, f := range inst.Folders {
		if err := removeFolder(base, f); err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(base, f)); err == nil {
			return nil, fmt.Errorf("%s could not be deleted (is the game running?)", filepath.Join(base, f))
		}
	}
	delete(m.State.Mods, key)
	m.logf("removed %s (deleted %s)", key, strings.Join(inst.Folders, ", "))
	if err := m.State.Save(); err != nil {
		return nil, err
	}
	var others []string
	if ents, err := os.ReadDir(base); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			id := readIdent(filepath.Join(base, e.Name()))
			for _, want := range idents {
				if want.same(id) {
					others = append(others, e.Name())
					m.logf("  another copy of this mod is still in %s (not installed by ppgmods; delete it there if you don't want it)", filepath.Join(base, e.Name()))
					break
				}
			}
		}
	}
	return others, nil
}

type modIdent struct{ ugc, name, author string }

func (a modIdent) same(b modIdent) bool {
	if a.ugc != "" && a.ugc == b.ugc {
		return true
	}
	return a.name != "" && strings.EqualFold(a.name, b.name) && strings.EqualFold(a.author, b.author)
}

// readIdent reads what identifies a mod folder: its mod.json, at the top or
// one level down.
func readIdent(dir string) modIdent {
	p := filepath.Join(dir, "mod.json")
	if _, err := os.Stat(p); err != nil {
		p = ""
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if q := filepath.Join(dir, e.Name(), "mod.json"); e.IsDir() {
				if _, err := os.Stat(q); err == nil {
					p = q
					break
				}
			}
		}
		if p == "" {
			return modIdent{}
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return modIdent{}
	}
	var mj struct {
		Name, Author       string
		CreatorUGCIdentity json.RawMessage
	}
	json.Unmarshal(trimBOM(b), &mj)
	return modIdent{name: strings.TrimSpace(mj.Name), author: strings.TrimSpace(mj.Author), ugc: UGCString(mj.CreatorUGCIdentity)}
}

// UGCString reads a Workshop ID written as a string or a number ("0" and
// null mean none). A number must not go through float64: 3801154351 would
// become 3.801154351e+09.
func UGCString(raw json.RawMessage) string {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "0" || s == "null" {
		return ""
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return s
}

// MissingFolders reports whether any of an installed item's folders is gone
// from the game folder (deleted by hand, by another program, or by malware).
func (m *Manager) MissingFolders(inst *Installed) bool {
	for _, f := range inst.Folders {
		if _, err := os.Stat(filepath.Join(m.dirFor(inst), f)); err != nil {
			return true
		}
	}
	return false
}

// Forget stops tracking an item without touching the game folders (for
// items whose folders are already gone).
func (m *Manager) Forget(key string) error {
	if m.State.Mods[key] == nil {
		return fmt.Errorf("%s is not installed", key)
	}
	delete(m.State.Mods, key)
	m.logf("stopped tracking %s", key)
	return m.State.Save()
}

// Problem is a verify result for one Mods/ folder.
type Problem struct {
	Folder string `json:"folder"`
	Issue  string `json:"issue"`
	Bad    bool   `json:"bad"`           // false for informational lines (clean unmanaged folders)
	Key    string `json:"key,omitempty"` // the installed item it belongs to, if any (can be restored)
}

// Verify re-hashes installed mods to detect tampering (the September worm
// inserted itself into other mods' files) and scans folders ppgmods did not
// install.
func (m *Manager) Verify() ([]Problem, error) {
	var probs []Problem
	for _, inst := range m.State.Sorted() {
		now := map[string]string{}
		for _, f := range inst.Folders {
			if _, err := os.Stat(filepath.Join(m.dirFor(inst), f)); err != nil {
				probs = append(probs, Problem{f, "missing", true, inst.Key})
				continue
			}
			if err := hashTree(filepath.Join(m.dirFor(inst), f), f, now); err != nil {
				return nil, err
			}
		}
		// A file whose name changed but whose contents didn't (a renamed
		// file, or a name stored differently) is not a problem.
		goneByHash := map[string]int{}
		for p, h := range inst.Files {
			if _, ok := now[p]; !ok {
				goneByHash[h]++
			}
		}
		renamed := map[string]bool{}
		for p, h := range now {
			if _, ok := inst.Files[p]; !ok && goneByHash[h] > 0 {
				goneByHash[h]--
				renamed[h] = true
			}
		}
		for p, h := range inst.Files {
			if g, ok := now[p]; !ok {
				if !renamed[h] {
					probs = append(probs, Problem{p, "file deleted since install", true, inst.Key})
				}
			} else if g != h {
				if path.Base(p) == "mod.json" && manifestOnlyToggled(m.dirFor(inst), p, inst.Files, now) {
					probs = append(probs, Problem{p, "rewritten since install (the game does this when you turn a mod on or off); its scripts are unchanged", false, inst.Key})
					continue
				}
				probs = append(probs, Problem{p, "file CHANGED since install (possible tampering)", true, inst.Key})
			}
		}
		for p, h := range now {
			if _, ok := inst.Files[p]; !ok && !renamed[h] {
				probs = append(probs, Problem{p, "NEW file appeared since install (possible injection)", true, inst.Key})
			}
		}
		// An adopted folder never went through the install checks (it is
		// adopted automatically as soon as it appears), so its hashes only
		// show it hasn't changed since: keep reporting what the scanner
		// finds in it, as for a folder that isn't tracked.
		if inst.Adopted {
			for _, f := range inst.Folders {
				dir := filepath.Join(m.dirFor(inst), f)
				if _, err := os.Stat(dir); err != nil {
					continue
				}
				rep, err := scan.DirWith(dir, m.scanOpts())
				if err != nil {
					return nil, err
				}
				if rep.Max() >= scan.High {
					probs = append(probs, Problem{f, fmt.Sprintf("found in the game folder, not installed through ppgmods' checks; scan max %s (%d findings)", rep.Max(), len(rep.Findings)), true, inst.Key})
				}
			}
		}
	}
	ents, err := os.ReadDir(m.ModsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range ents {
		if !e.IsDir() || m.State.OwnerOf(KindMod, e.Name()) != nil {
			continue
		}
		rep, err := scan.DirWith(filepath.Join(m.ModsDir, e.Name()), m.scanOpts())
		if err != nil {
			return nil, err
		}
		pr := Problem{e.Name(), "not managed by ppgmods; scan clean", false, ""}
		if rep.Max() >= scan.Medium {
			pr.Issue = fmt.Sprintf("not managed by ppgmods; scan max %s (%d findings)", rep.Max(), len(rep.Findings))
			pr.Bad = rep.Max() >= scan.High
		}
		probs = append(probs, pr)
	}
	probs = append(probs, m.verifyGameCode()...)
	return probs, nil
}

// manifestOnlyToggled reports whether a changed mod.json still lists only
// scripts that were installed and haven't changed: the game rewrites
// mod.json itself (its "Active" switch), while an injection adds or changes
// scripts, which shows up on those files.
func manifestOnlyToggled(base, key string, installed, now map[string]string) bool {
	b, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(key)))
	if err != nil {
		return false
	}
	var mf struct {
		Scripts []string `json:"Scripts"`
	}
	if json.Unmarshal(b, &mf) != nil {
		return false
	}
	dir := path.Dir(key)
	for _, s := range mf.Scripts {
		k := path.Join(dir, filepath.ToSlash(s))
		if h, ok := installed[k]; !ok || now[k] != h {
			return false
		}
	}
	return true
}

// verifyGameCode checks the game's own code folders for what the FPS++
// worms left there: infected compiled mods in CompiledMods (where the game
// keeps the mods it compiled) and DLLs dropped into People
// Playground_Data/Managed. Deleting CompiledMods is safe (the game compiles
// the mods again); a worm DLL in Managed means reinstalling the game.
func (m *Manager) verifyGameCode() []Problem {
	if m.ModsDir == "" {
		return nil
	}
	game := filepath.Dir(m.ModsDir)
	var probs []Problem
	// mods: compiled mods get the full worm check; the game's and the
	// loaders' own files only the blocklist and worm-only strings, since
	// they define the very APIs the worm used.
	mods := false
	checkFile := func(p, label, fix string) {
		why := ""
		if h, err := fileSHA(p); err == nil && m.Blocklist != nil {
			if b, ok := m.Blocklist.sha[h]; ok {
				why = "blocklisted: " + b.Reason
			}
		}
		if why == "" && mods {
			why = scan.WormDLL(p)
		}
		if why == "" {
			why = scan.WormOnlyDLL(p)
		}
		if why != "" {
			probs = append(probs, Problem{label, "WORM: " + why + ". " + fix, true, ""})
		}
	}
	check := func(dir, label, fix string) {
		for _, rel := range gameDLLs(dir) {
			checkFile(filepath.Join(dir, filepath.FromSlash(rel)), label+"/"+rel, fix)
		}
	}
	mods = true
	check(filepath.Join(game, "CompiledMods"), "CompiledMods", "Delete the CompiledMods folder (the game rebuilds it), remove the mod it came from, and reset your Discord and Steam passwords.")
	check(filepath.Join(game, "CompiledModAssemblies"), "CompiledModAssemblies", "Delete that folder (the game rebuilds it) and remove the mod it came from.")
	mods = false
	check(filepath.Join(game, "People Playground_Data", "Managed"), "People Playground_Data/Managed", "Reinstall People Playground (in Steam: Properties, Installed Files, Verify integrity of game files) and reset your Discord and Steam passwords.")
	// BepInEx (used by RE_PPG) runs plugins before every mod and before the
	// game's own checks: the most valuable place for malware to sit.
	bepFix := "Delete that file, reinstall BepInEx and RE_PPG from their official releases, and reset your Discord and Steam passwords."
	for _, sub := range []string{"plugins", "patchers", "core"} {
		check(filepath.Join(game, "BepInEx", sub), "BepInEx/"+sub, bepFix)
	}
	check(filepath.Join(game, "RE_PPG"), "RE_PPG", bepFix)
	for _, f := range []string{"winhttp.dll", "version.dll", "doorstop.dll"} {
		if _, err := os.Stat(filepath.Join(game, f)); err == nil {
			checkFile(filepath.Join(game, f), f, bepFix)
		}
	}
	probs = append(probs, m.verifyLoaderFiles(game)...)
	flagged := map[string]bool{}
	for _, p := range probs {
		flagged[p.Folder] = true
	}
	l := gameDetect(game)
	for _, kind := range []struct {
		dir  string
		list []string
	}{{"plugins", l.Plugins}, {"patchers", l.Patchers}} {
		for _, rel := range kind.list {
			up := strings.ToUpper(rel)
			if strings.Contains(up, "RE_PPG") || flagged["BepInEx/"+kind.dir+"/"+rel] {
				continue
			}
			probs = append(probs, Problem{"BepInEx/" + kind.dir + "/" + rel,
				"BepInEx " + strings.TrimSuffix(kind.dir, "s") + " that is not part of RE_PPG: it runs before every mod with full access to your PC. Keep it only if you installed it on purpose.", false, ""})
		}
	}
	return probs
}

// verifyLoaderFiles compares RE_PPG's and BepInEx's files with their
// official releases: a file that matches none was replaced or added by
// something else, unless it is newer than the list.
func (m *Manager) verifyLoaderFiles(gameDir string) []Problem {
	if m.Releases == nil {
		return nil
	}
	var files []string // relative to the game folder
	add := func(rel string) {
		if fi, err := os.Stat(filepath.Join(gameDir, rel)); err == nil && !fi.IsDir() {
			files = append(files, filepath.ToSlash(rel))
		}
	}
	walk := func(dir string, all bool) {
		filepath.WalkDir(filepath.Join(gameDir, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			rel, _ := filepath.Rel(gameDir, p)
			if (ext == ".dll" || ext == ".exe") && (all || strings.Contains(strings.ToUpper(rel), "RE_PPG")) {
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	walk("RE_PPG", true)
	walk(filepath.Join("BepInEx", "core"), true)
	walk(filepath.Join("BepInEx", "plugins"), false)
	walk(filepath.Join("BepInEx", "patchers"), false)
	for _, f := range []string{"winhttp.dll", "doorstop.dll", "libdoorstop.so", "libdoorstop.dylib"} {
		add(f)
	}
	if len(files) == 0 {
		return nil
	}
	ix := m.Releases()
	if ix == nil || len(ix.Files) == 0 {
		return []Problem{{"RE_PPG / BepInEx", "could not get the list of official releases to compare their files with (offline?)", false, ""}}
	}
	var probs []Problem
	versions := map[string]bool{}
	matched := 0
	for _, rel := range files {
		p := filepath.Join(gameDir, filepath.FromSlash(rel))
		h, err := fileSHA(p)
		if err != nil {
			continue
		}
		if off := ix.Lookup(h); len(off) > 0 {
			matched++
			versions[off[0].Project+" "+off[0].Version] = true
			continue
		}
		fi, _ := os.Stat(p)
		if fi != nil && ix.Updated.After(fi.ModTime().Add(12*time.Hour)) {
			probs = append(probs, Problem{rel, "matches no official RE_PPG or BepInEx release: something replaced or added it. Reinstall RE_PPG from its official release (it reinstalls BepInEx) and check your PC for malware.", true, ""})
		} else {
			probs = append(probs, Problem{rel, "newer than our list of official RE_PPG and BepInEx releases (updated " + ix.Updated.Format("2006-01-02 15:04") + " UTC); if RE_PPG just updated itself, run Verify again later.", false, ""})
		}
	}
	if matched > 0 {
		var vs []string
		for v := range versions {
			vs = append(vs, v)
		}
		sort.Strings(vs)
		probs = append(probs, Problem{"RE_PPG / BepInEx", fmt.Sprintf("%d files match official releases (%s)", matched, strings.Join(vs, ", ")), false, ""})
	}
	return probs
}

var (
	gameDLLs   = game.DLLsUnder
	gameDetect = game.DetectLoader
)

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

// Overridable reports whether a refusal reason may be overridden by a person
// from the GUI, for one install: HIGH findings, the cooldown and new findings
// in an update. CRITICAL findings and the worm cutoff need the command line;
// the blocklist, failed source checks and checksum mismatches never can be.
func Overridable(reason string) bool {
	return RiskReason(reason) || strings.Contains(reason, "--allow-high") || strings.Contains(reason, "--cooldown") || strings.Contains(reason, "--allow-new-findings")
}

// RiskReason reports a CRITICAL refusal the person may accept in the window
// (with a typed confirmation).
func RiskReason(reason string) bool { return strings.Contains(reason, "override: accept the risk") }

// wormRules are what the September 2026 worm did: spread through Workshop
// uploads, spam friends, take over accounts, copy itself into other mods and
// delete game files. A finding like this is never accepted from the window.
var wormRules = map[string]bool{
	"steam-ugc": true, "steam-friends": true, "steam-auth": true, "self-replication": true,
	"game-path-tamper": true, "mass-delete": true, "encoded-code": true, "symlink": true,
	"deserialization": true, "json-gadget": true, "embedded-executable": true, "disables-protection": true, "worm-dll": true, "blocklisted": true,
}

func wormFindings(rep *scan.Report) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range rep.Findings {
		if f.Severity >= scan.Critical && wormRules[f.Rule] && !seen[f.Rule] {
			seen[f.Rule] = true
			out = append(out, f.Rule)
		}
	}
	return out
}

const (
	KindMod         = "mod"
	KindContraption = "contraption"
)

// dirFor is the game folder an installed item lives in.
func (m *Manager) dirFor(inst *Installed) string {
	if inst.Kind == KindContraption {
		return m.ContraptionsDir
	}
	return m.ModsDir
}

// contraptionFiles are the files a saved contraption consists of. Nothing
// else from a contraption archive is installed.
var contraptionFiles = map[string]bool{".jaap": true, ".json": true, ".outline": true, ".png": true, ".jpg": true}

// contraptionRoots finds contraptions (<name>.jaap plus <name>.json,
// .outline and .png) under dir and stages each in its own folder named
// after it, the layout Contraptions/<name>/<name>.jaap the game uses.
func contraptionRoots(dir string) (roots, names []string, err error) {
	var jaaps []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".jaap") {
			jaaps = append(jaaps, p)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	stage := filepath.Join(dir, ".ppgmods-contraptions")
	for i, j := range jaaps {
		base := strings.TrimSuffix(filepath.Base(j), filepath.Ext(j))
		name := strings.TrimRight(strings.TrimSpace(reUnsafeName.ReplaceAllString(base, "_")), ". ")
		if name == "" || archive.WindowsUnsafe(name) {
			name = fmt.Sprintf("contraption %d", i+1)
		}
		unit := filepath.Join(stage, fmt.Sprint(i), name)
		if err := os.MkdirAll(unit, 0o755); err != nil {
			return nil, nil, err
		}
		ents, err := os.ReadDir(filepath.Dir(j))
		if err != nil {
			return nil, nil, err
		}
		for _, e := range ents {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			stem := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			if e.IsDir() || !contraptionFiles[ext] || !strings.EqualFold(stem, base) {
				continue
			}
			if err := copyTree(filepath.Join(filepath.Dir(j), e.Name()), filepath.Join(unit, name+ext)); err != nil {
				return nil, nil, err
			}
		}
		roots, names = append(roots, unit), append(names, name)
	}
	return roots, names, nil
}

// Adopt starts tracking a folder that is already in Mods/ or Contraptions/
// (installed by hand or by another tool): it is scanned and fingerprinted
// but not moved or changed.
func (m *Manager) Adopt(key, name, author, source, version, kind, folder string, aliases ...string) (*scan.Report, error) {
	base := m.ModsDir
	if kind == KindContraption {
		base = m.ContraptionsDir
	}
	dir := filepath.Join(base, folder)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	if owner := m.State.OwnerOf(kind, folder); owner != nil {
		return nil, fmt.Errorf("%s is already tracked as %s", folder, owner.Key)
	}
	if prev := m.State.Mods[key]; prev != nil {
		return nil, fmt.Errorf("%s is already installed (in %s)", key, strings.Join(prev.Folders, ", "))
	}
	rep, err := scan.DirWith(dir, m.scanOpts())
	if err != nil {
		return nil, err
	}
	inst := &Installed{
		Key: key, Name: name, Author: author, Source: source, Version: version, Kind: kind, Folders: []string{folder},
		Files: map[string]string{}, InstalledAt: time.Now().UTC(), Adopted: true, ScanMax: maxName(rep), Aliases: aliases,
	}
	for k := range rep.Keys() {
		inst.Findings = append(inst.Findings, k)
	}
	if err := hashTree(dir, folder, inst.Files); err != nil {
		return nil, err
	}
	m.State.Mods[key] = inst
	return rep, m.State.Save()
}

// workshopIDIn returns the Steam Workshop ID (mod.json CreatorUGCIdentity)
// of the mod in dir, if it has one.
func workshopIDIn(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "mod.json"))
	if err != nil {
		return ""
	}
	var mj struct{ CreatorUGCIdentity json.RawMessage }
	if json.Unmarshal(trimBOM(b), &mj) != nil {
		return ""
	}
	u := UGCString(mj.CreatorUGCIdentity)
	if len(u) < 6 || len(u) > 12 || strings.Trim(u, "0123456789") != "" {
		return ""
	}
	return u
}

// modJSONNames returns the Name and Author fields of the mod.json in dir.
func modJSONNames(dir string) (name, author string) {
	b, err := os.ReadFile(filepath.Join(dir, "mod.json"))
	if err != nil {
		return "", ""
	}
	var mj struct{ Name, Author string }
	json.Unmarshal(trimBOM(b), &mj)
	return strings.TrimSpace(mj.Name), strings.TrimSpace(mj.Author)
}

// scanOpts lets the scanner compare bundled DLLs with the game's own.
func (m *Manager) scanOpts() scan.Options {
	if m.ModsDir == "" {
		return scan.Options{}
	}
	return scan.Options{GameManaged: filepath.Join(filepath.Dir(m.ModsDir), "People Playground_Data", "Managed")}
}

// removeFolder deletes base/name, where name comes from state.json: only a
// plain folder name directly inside the Mods or Contraptions folder.
func removeFolder(base, name string) error {
	if base == "" || name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return fmt.Errorf("refusing to delete %q: not a folder inside %s", name, base)
	}
	return os.RemoveAll(filepath.Join(base, name))
}
