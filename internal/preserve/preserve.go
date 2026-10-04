// Package preserve is the pre-worm archive: a record of every Steam
// Workshop copy the mirrors (Skymods, top-mods) hold from before the worm,
// with its SHA-256, size, contents and scan result.
//
// The record proves which copies are genuine: a mirror file that no longer
// matches its record was changed after it was archived, and ppgmods refuses
// it. When the archive also stores files (an opt-in, see cmd/preserve), a
// copy that disappears from its mirror can still be installed. Authors who
// don't want their work kept are left out (archive/optout.txt).
package preserve

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Repo and branch the archive is published on.
const (
	Repo       = "DogeKingC/SWG"
	DataBranch = "archive-data"
)

// Record is one mirror copy.
type Record struct {
	Mirror     string    `json:"mirror"` // skymods:<id> or topmods:<id>
	WorkshopID string    `json:"ws"`
	Title      string    `json:"title"`
	Author     string    `json:"author,omitempty"`
	Revision   time.Time `json:"revision,omitempty"` // Steam revision of the copy
	PreWorm    bool      `json:"pre_worm"`
	SHA256     string    `json:"sha256,omitempty"`
	Size       int64     `json:"size,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	ModVersion string    `json:"mod_version,omitempty"`
	UGC        string    `json:"ugc,omitempty"` // mod.json's Workshop ID
	ScanMax    string    `json:"scan_max,omitempty"`
	Rules      []string  `json:"rules,omitempty"` // scanner rules found (MEDIUM and up)
	Archived   time.Time `json:"archived"`
	Stored     string    `json:"stored,omitempty"` // permanent copy, when files are stored
	Gone       bool      `json:"gone,omitempty"`   // the mirror didn't serve the file
	Error      string    `json:"error,omitempty"`
}

// Archive is archive.json.
type Archive struct {
	Updated time.Time          `json:"updated"`
	Records map[string]*Record `json:"records"` // by mirror ID
	Cursor  Cursor             `json:"cursor"`
}

// Cursor is where the next run continues.
type Cursor struct {
	SkyPage  int `json:"sky_page"`
	TMOffset int `json:"tm_offset"`
}

// Stats counts what the archive holds.
func (a *Archive) Stats() (total, preWorm, hashed, stored int) {
	for _, r := range a.Records {
		total++
		if r.PreWorm {
			preWorm++
		}
		if r.SHA256 != "" {
			hashed++
		}
		if r.Stored != "" {
			stored++
		}
	}
	return
}

var (
	mu      sync.Mutex
	cached  *Archive
	fetched time.Time
	httpc   = &http.Client{Timeout: 2 * time.Minute}
	// CacheDir keeps the last archive for offline use ("" disables).
	CacheDir string
)

// Fetch returns the published archive, refreshed every 12 hours.
func Fetch() (*Archive, error) {
	mu.Lock()
	defer mu.Unlock()
	if cached != nil && time.Since(fetched) < 12*time.Hour {
		return cached, nil
	}
	a, err := download()
	if err == nil {
		cached, fetched = a, time.Now()
		return a, nil
	}
	if cached != nil {
		return cached, nil
	}
	if CacheDir != "" {
		if b, rerr := os.ReadFile(filepath.Join(CacheDir, "archive.json")); rerr == nil {
			var a Archive
			if json.Unmarshal(b, &a) == nil {
				cached, fetched = &a, time.Now().Add(-11*time.Hour)
				return cached, nil
			}
		}
	}
	return nil, err
}

// Lookup returns the record of a mirror copy from the archive already
// fetched (nil if unknown or the archive isn't loaded).
func Lookup(mirrorID string) *Record {
	a, err := Fetch()
	if err != nil || a == nil {
		return nil
	}
	return a.Records[mirrorID]
}

func download() (*Archive, error) {
	ref := DataBranch
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+Repo+"/commits/"+DataBranch, nil)
	req.Header.Set("Accept", "application/vnd.github.sha")
	req.Header.Set("User-Agent", "ppgmods")
	if resp, err := httpc.Do(req); err == nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
		resp.Body.Close()
		if sha := strings.TrimSpace(string(b)); resp.StatusCode == 200 && len(sha) == 40 {
			ref = sha
		} else if resp.StatusCode == 404 || resp.StatusCode == 422 {
			return nil, errors.New("the pre-worm archive isn't published yet")
		}
	}
	req, _ = http.NewRequest("GET", "https://raw.githubusercontent.com/"+Repo+"/"+ref+"/archive.json", nil)
	req.Header.Set("User-Agent", "ppgmods")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("pre-worm archive: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if err != nil {
		return nil, err
	}
	var a Archive
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	if CacheDir != "" {
		os.MkdirAll(CacheDir, 0o755)
		os.WriteFile(filepath.Join(CacheDir, "archive.json"), b, 0o644)
	}
	return &a, nil
}

// Cached is Lookup without fetching: it only consults an archive already
// loaded.
func Cached(mirrorID string) *Record {
	mu.Lock()
	defer mu.Unlock()
	if cached == nil {
		return nil
	}
	return cached.Records[mirrorID]
}

// SetForTest replaces the archive (tests).
func SetForTest(a *Archive) func() {
	mu.Lock()
	old, oldAt := cached, fetched
	cached, fetched = a, time.Now().Add(100*365*24*time.Hour)
	mu.Unlock()
	return func() {
		mu.Lock()
		cached, fetched = old, oldAt
		mu.Unlock()
	}
}

// OptOut is archive/optout.txt: Workshop IDs, or "author: <name>", one per
// line. Those mods are never archived.
type OptOut struct {
	ids     map[string]bool
	authors map[string]bool
}

// LoadOptOut reads an opt-out list (missing file: nothing opted out).
func LoadOptOut(path string) *OptOut {
	o := &OptOut{ids: map[string]bool{}, authors: map[string]bool{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return o
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "":
		case strings.HasPrefix(strings.ToLower(line), "author:"):
			o.authors[strings.ToLower(strings.TrimSpace(line[len("author:"):]))] = true
		default:
			o.ids[line] = true
		}
	}
	return o
}

// Excludes reports whether a mod is opted out.
func (o *OptOut) Excludes(ws, author string) bool {
	return o.ids[ws] || author != "" && o.authors[strings.ToLower(strings.TrimSpace(author))]
}
