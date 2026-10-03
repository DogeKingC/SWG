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

type Blocklist struct {
	Entries []Entry `json:"entries"`
	sha     map[string]Entry
}

func parseBlocklist(b []byte) ([]Entry, error) {
	var bl Blocklist
	if err := json.Unmarshal(b, &bl); err != nil {
		return nil, err
	}
	return bl.Entries, nil
}

// LoadBlocklist merges the embedded list, the latest copy from the
// repository (cached for offline use) and the user's local blocklist.json.
// Because the list can only deny, a tampered remote copy cannot make an
// unsafe mod install.
func LoadBlocklist(fetch bool, logf func(string, ...any)) *Blocklist {
	bl := &Blocklist{sha: map[string]Entry{}}
	add := func(src string, b []byte) {
		es, err := parseBlocklist(b)
		if err != nil {
			logf("warning: ignoring %s blocklist: %v", src, err)
			return
		}
		bl.Entries = append(bl.Entries, es...)
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
	for _, e := range bl.Entries {
		switch {
		case e.WorkshopID != "" && c.Key == "sky:"+e.WorkshopID,
			e.GameBananaMod != 0 && c.Key == fmt.Sprintf("gb:%d", e.GameBananaMod),
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
