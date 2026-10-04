// Package loaders is the list of official RE_PPG and BepInEx release files:
// the SHA-256 of every file in their release archives, collected daily by
// cmd/loaders (loaders.json on the loaders-data branch). ppgmods compares the
// loader files in the game folder against it, so a replaced or added file
// in RE_PPG or BepInEx stands out.
package loaders

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

const (
	Repo       = "Trlydev/SWG"
	DataBranch = "loaders-data"
)

// File is one file of an official release.
type File struct {
	Project string `json:"project"` // RE_PPG or BepInEx
	Version string `json:"version"`
	Path    string `json:"path"` // inside the release archive
}

// Index is loaders.json.
type Index struct {
	Updated  time.Time         `json:"updated"`
	Releases []string          `json:"releases"` // "RE_PPG v0.2.16", "BepInEx v5.4.23.5"
	Files    map[string][]File `json:"files"`    // by SHA-256
}

// Lookup returns the official files with this SHA-256.
func (ix *Index) Lookup(sha string) []File {
	if ix == nil {
		return nil
	}
	return ix.Files[strings.ToLower(sha)]
}

// Add records a file (deduplicated).
func (ix *Index) Add(sha string, f File) {
	if ix.Files == nil {
		ix.Files = map[string][]File{}
	}
	sha = strings.ToLower(sha)
	for _, e := range ix.Files[sha] {
		if e == f {
			return
		}
	}
	ix.Files[sha] = append(ix.Files[sha], f)
}

// HasRelease reports whether a release was indexed.
func (ix *Index) HasRelease(name string) bool {
	for _, r := range ix.Releases {
		if r == name {
			return true
		}
	}
	return false
}

var (
	mu      sync.Mutex
	cached  *Index
	fetched time.Time
	httpc   = &http.Client{Timeout: time.Minute}
	// CacheDir keeps the last index for offline use ("" disables).
	CacheDir string
)

// Fetch returns the published index, refreshed every 6 hours.
func Fetch() (*Index, error) {
	mu.Lock()
	defer mu.Unlock()
	if cached != nil && time.Since(fetched) < 6*time.Hour {
		return cached, nil
	}
	ix, err := download()
	if err == nil {
		cached, fetched = ix, time.Now()
		return ix, nil
	}
	if cached != nil {
		return cached, nil
	}
	if CacheDir != "" {
		if b, rerr := os.ReadFile(filepath.Join(CacheDir, "loaders.json")); rerr == nil {
			var ix Index
			if json.Unmarshal(b, &ix) == nil {
				cached, fetched = &ix, time.Now().Add(-5*time.Hour)
				return cached, nil
			}
		}
	}
	return nil, err
}

func download() (*Index, error) {
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
			return nil, errors.New("the list of official loader releases isn't published yet")
		}
	}
	req, _ = http.NewRequest("GET", "https://raw.githubusercontent.com/"+Repo+"/"+ref+"/loaders.json", nil)
	req.Header.Set("User-Agent", "ppgmods")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("official loader releases: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, err
	}
	if CacheDir != "" {
		os.MkdirAll(CacheDir, 0o755)
		os.WriteFile(filepath.Join(CacheDir, "loaders.json"), b, 0o644)
	}
	return &ix, nil
}

// SetForTest replaces the index (tests).
func SetForTest(ix *Index) func() {
	mu.Lock()
	old, oldAt := cached, fetched
	cached, fetched = ix, time.Now().Add(100*365*24*time.Hour)
	mu.Unlock()
	return func() {
		mu.Lock()
		cached, fetched = old, oldAt
		mu.Unlock()
	}
}
