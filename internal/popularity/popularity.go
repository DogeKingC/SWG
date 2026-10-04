// Package popularity ranks mods by how much attention they got recently.
//
// None of the sites says what is popular this week, and ppgmods has no
// server or database. Instead a scheduled GitHub Action (cmd/popindex)
// records each site's public counters once a day: GameBanana views, True
// Workshop downloads and top-mods views. The difference between today's
// count and the count 1, 7 or 30 days ago is the popularity for that period.
// Snapshots older than Retention are deleted, and the data branch is
// rewritten as a single commit, so the history never grows.
package popularity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Retention is how many days of daily snapshots are kept.
const Retention = 31

// IndexURL is where the Action publishes the ranking.
const IndexURL = "https://raw.githubusercontent.com/DogeKingC/SWG/popularity-data/popularity.json"

// Periods and their length in days.
var Periods = map[string]int{"day": 1, "week": 7, "month": 30}

// Snapshot is one day's counters.
type Snapshot struct {
	Date    string         `json:"date"`    // 2006-01-02 (UTC)
	Sources []string       `json:"sources"` // sources collected successfully that day
	Counts  map[string]int `json:"counts"`  // ref -> counter
}

// Item is a ranked entry with what a search card needs.
type Item struct {
	Ref    string `json:"ref"` // gb:<id>, tw:<id>, sky:<workshop id>
	Src    string `json:"src"` // gb, tw or tm
	Name   string `json:"name"`
	Author string `json:"author,omitempty"`
	Image  string `json:"image,omitempty"`
	Kind   string `json:"kind"` // mod, contraption or other (not installable)
	// KindChecked: Kind comes from the archive's contents, not a category.
	KindChecked bool   `json:"kind_checked,omitempty"`
	Category    string `json:"category,omitempty"`
	Date        string `json:"date,omitempty"`
	URL         string `json:"url,omitempty"`
	Reviewed    bool   `json:"reviewed,omitempty"`
	N           int    `json:"n"`           // counter today (all time)
	D           int    `json:"d,omitempty"` // gained in the last day
	W           int    `json:"w,omitempty"` // ... week
	M           int    `json:"m,omitempty"` // ... 30 days
}

// Index is the published ranking.
type Index struct {
	Generated time.Time         `json:"generated"`
	Since     string            `json:"since"` // oldest snapshot used
	Metric    map[string]string `json:"metric"`
	Items     []Item            `json:"items"`
}

// Metric names what each source's counter counts.
var Metric = map[string]string{"gb": "views", "tw": "downloads", "tm": "views", "s01": "views", "nx": "downloads"}

// Compute fills each item's D/W/M from the snapshots (oldest first; the last
// one is today's).
func Compute(items []Item, snaps []Snapshot) {
	if len(snaps) == 0 {
		return
	}
	today := snaps[len(snaps)-1]
	t0, _ := time.Parse("2006-01-02", today.Date)
	for i := range items {
		it := &items[i]
		gain := func(days int) int {
			base := baseline(snaps, t0.AddDate(0, 0, -days), it.Src)
			if base == nil {
				return 0
			}
			old, ok := base.Counts[it.Ref]
			if !ok {
				old = 0 // new since then: everything it has counts
			}
			if d := it.N - old; d > 0 {
				return d
			}
			return 0
		}
		it.D, it.W, it.M = gain(1), gain(7), gain(30)
	}
}

// baseline is the newest snapshot taken on or before day that has src;
// if history is shorter than the period, the oldest snapshot with src.
func baseline(snaps []Snapshot, day time.Time, src string) *Snapshot {
	var oldest, best *Snapshot
	for i := range snaps[:len(snaps)-1] {
		s := &snaps[i]
		if !has(s.Sources, src) {
			continue
		}
		if oldest == nil {
			oldest = s
		}
		if d, err := time.Parse("2006-01-02", s.Date); err == nil && !d.After(day) {
			best = s
		}
	}
	if best != nil {
		return best
	}
	return oldest
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Gain returns an item's count for a period ("day", "week", "month";
// anything else is all time).
func (it Item) Gain(period string) int {
	switch period {
	case "day":
		return it.D
	case "week":
		return it.W
	case "month":
		return it.M
	}
	return it.N
}

// Query filters and ranks items: one source, one kind, names containing
// every word of q; most gained in the period first (ties: all-time count).
func (ix *Index) Query(src, kind, q, period string, offset, limit int) []Item {
	words := strings.Fields(strings.ToLower(q))
	var out []Item
	for _, it := range ix.Items {
		if it.Src != src || (kind != "" && it.Kind != kind) {
			continue
		}
		name := strings.ToLower(it.Name + " " + it.Author)
		ok := true
		for _, w := range words {
			if !strings.Contains(name, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].Gain(period), out[j].Gain(period); a != b {
			return a > b
		}
		return out[i].N > out[j].N
	})
	if offset >= len(out) {
		return nil
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Days is how many days of history the index covers.
func (ix *Index) Days() int {
	d, err := time.Parse("2006-01-02", ix.Since)
	if err != nil {
		return 0
	}
	return int(ix.Generated.Sub(d).Hours() / 24)
}

var (
	mu      sync.Mutex
	cached  *Index
	fetched time.Time
	// CacheDir keeps the last index for offline use ("" disables).
	CacheDir string
	client   = &http.Client{Timeout: 30 * time.Second}
)

// Fetch returns the published index, refreshed at most once an hour.
func Fetch() (*Index, error) {
	mu.Lock()
	defer mu.Unlock()
	if cached != nil && time.Since(fetched) < time.Hour {
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
		if b, rerr := os.ReadFile(filepath.Join(CacheDir, "popularity.json")); rerr == nil {
			var ix Index
			if json.Unmarshal(b, &ix) == nil {
				cached, fetched = &ix, time.Now().Add(-50*time.Minute) // retry in 10 minutes
				return cached, nil
			}
		}
	}
	return nil, err
}

// latestURL is popularity.json at the branch's newest commit. The branch
// URL is cached by GitHub's CDN for a few minutes after each daily run, and
// a stale copy would then be kept for an hour; a commit URL never changes.
func latestURL() string {
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/DogeKingC/SWG/commits/popularity-data", nil)
	req.Header.Set("Accept", "application/vnd.github.sha")
	req.Header.Set("User-Agent", "ppgmods")
	resp, err := client.Do(req)
	if err != nil {
		return IndexURL
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
	sha := strings.TrimSpace(string(b))
	if resp.StatusCode != 200 || len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
		return IndexURL
	}
	return "https://raw.githubusercontent.com/DogeKingC/SWG/" + sha + "/popularity.json"
}

func download() (*Index, error) {
	req, _ := http.NewRequest("GET", latestURL(), nil)
	req.Header.Set("User-Agent", "ppgmods")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("no popularity data published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("popularity data: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, err
	}
	if CacheDir != "" {
		os.WriteFile(filepath.Join(CacheDir, "popularity.json"), b, 0o644)
	}
	return &ix, nil
}
