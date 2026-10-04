package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DogeKingC/SWG/blocklist"
	"github.com/DogeKingC/SWG/internal/scan"
)

// Entry blocks one artifact. Exactly one of the ID fields is normally set.
type Entry struct {
	SHA256         string `json:"sha256,omitempty"`
	WorkshopID     string `json:"workshop_id,omitempty"`
	GameBananaMod  int    `json:"gamebanana_mod,omitempty"`
	GameBananaFile int    `json:"gamebanana_file,omitempty"`
	Reason         string `json:"reason"`
}

// Pause stops every install and update from the named sources while a
// worm may be spreading through them. Like the rest of the list it can
// only refuse, so a tampered copy can stop installs but never allow one.
type Pause struct {
	// Sources are ref prefixes: "gb", "sky" (Steam Workshop copies), "tw",
	// "nx", "ow", "local" (files on this PC), or "*" for all of them.
	Sources []string `json:"sources"`
	Reason  string   `json:"reason"`
}

type Blocklist struct {
	Entries []Entry `json:"entries"`
	Pauses  []Pause `json:"pause,omitempty"`
	sha     map[string]Entry
}

func parseBlocklist(b []byte) (*Blocklist, error) {
	var bl Blocklist
	if err := json.Unmarshal(b, &bl); err != nil {
		return nil, err
	}
	return &bl, nil
}

// lastPauses is the pause notice last logged, so it is logged once, not
// on every action.
var lastPauses string

// Paused returns why installs from a ref are paused, or "".
func (bl *Blocklist) Paused(ref string) string {
	if bl == nil {
		return ""
	}
	src, _, _ := strings.Cut(ref, ":")
	for _, p := range bl.Pauses {
		for _, s := range p.Sources {
			if s == "*" || strings.EqualFold(s, src) {
				return p.Reason
			}
		}
	}
	return ""
}

// LoadBlocklist merges the embedded list, the latest copy from the
// repository (cached for offline use) and the user's local blocklist.json.
// Because the list can only deny, a tampered remote copy cannot make an
// unsafe mod install.
func LoadBlocklist(fetch bool, logf func(string, ...any)) *Blocklist {
	bl := &Blocklist{sha: map[string]Entry{}}
	add := func(src string, b []byte) {
		got, err := parseBlocklist(b)
		if err != nil {
			logf("warning: ignoring %s blocklist: %v", src, err)
			return
		}
		bl.Entries = append(bl.Entries, got.Entries...)
		bl.Pauses = append(bl.Pauses, got.Pauses...)
	}
	add("embedded", blocklist.Default)
	dir, err := ConfigDir()
	if err == nil {
		cache := filepath.Join(dir, "blocklist-remote.json")
		if fetch {
			if b, err := fetchRemote(); err == nil {
				os.WriteFile(cache, b, 0o644)
			} else {
				logf("warning: could not refresh blocklist (%v); using cached copy", err)
			}
		}
		if b, err := os.ReadFile(cache); err == nil {
			add("cached remote", b)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "blocklist.json")); err == nil {
			add("local", b)
		}
	}
	for _, e := range bl.Entries {
		if e.SHA256 != "" {
			bl.sha[strings.ToLower(e.SHA256)] = e
		}
	}
	var paused []string
	for _, p := range bl.Pauses {
		paused = append(paused, fmt.Sprintf("INSTALLS PAUSED from %s: %s", strings.Join(p.Sources, ", "), p.Reason))
	}
	if now := strings.Join(paused, "\n"); now != lastPauses {
		lastPauses = now
		for _, l := range paused {
			logf("%s", l)
		}
	}
	return bl
}

func fetchRemote() ([]byte, error) {
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Get(blocklist.RemoteURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if _, err := parseBlocklist(b); err != nil {
		return nil, err
	}
	return b, nil
}

// Check adds a "blocklisted" finding for any match against the candidate's
// identity, archive hash or the hash of any staged file.
func (bl *Blocklist) Check(c *Candidate, dir string, rep *scan.Report) {
	if bl == nil {
		return
	}
	hit := func(file string, e Entry) {
		rep.Findings = append([]scan.Finding{{Severity: scan.Critical, Rule: "blocklisted", File: file, Detail: "blocklisted: " + e.Reason}}, rep.Findings...)
	}
	for _, ref := range append([]string{c.Key}, c.Aliases...) {
		if why := bl.Paused(ref); why != "" {
			hit("", Entry{Reason: "installs from this source are paused by the ppgmods maintainers: " + why})
			break
		}
	}
	// A blocked Workshop item is the same mod when it comes from a mirror,
	// another site or a file on disk: match every ref the candidate goes by
	// and the Workshop ID its mod.json carries, not only the key it was
	// fetched under.
	refs := map[string]bool{c.Key: true}
	for _, a := range c.Aliases {
		refs[a] = true
	}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(d.Name(), "mod.json") {
			if b, err := os.ReadFile(p); err == nil {
				var mj struct{ CreatorUGCIdentity json.RawMessage }
				if json.Unmarshal(trimBOM(b), &mj) == nil {
					if u := UGCString(mj.CreatorUGCIdentity); u != "" {
						refs["sky:"+u] = true
					}
				}
			}
		}
		return nil
	})
	for _, e := range bl.Entries {
		switch {
		case e.WorkshopID != "" && refs["sky:"+e.WorkshopID],
			e.GameBananaMod != 0 && refs[fmt.Sprintf("gb:%d", e.GameBananaMod)],
			e.GameBananaFile != 0 && c.FileID == e.GameBananaFile && strings.HasPrefix(c.Key, "gb:"):
			hit("", e)
		}
	}
	if e, ok := bl.sha[strings.ToLower(c.ArchiveSHA)]; ok && c.ArchiveSHA != "" {
		hit(filepath.Base(c.Path), e)
	}
	if len(bl.sha) == 0 {
		return
	}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if h, err := fileSHA(p); err == nil {
			if e, ok := bl.sha[h]; ok {
				rel, _ := filepath.Rel(dir, p)
				hit(rel, e)
			}
		}
		return nil
	})
}
