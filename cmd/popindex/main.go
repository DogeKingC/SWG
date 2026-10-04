// popindex records each site's public popularity counters once a day and
// publishes the ranking ppgmods uses for "Popular today / this week / this
// month". It runs in a scheduled GitHub Action (see
// .github/workflows/popularity.yml) on the popularity-data branch:
//
//	popindex -dir <checkout of popularity-data>
//
// The directory holds snapshots/<date>.json (deleted after
// popularity.Retention days), topmods.json (top-mods item -> Workshop ID,
// so each item page is read once) and the published popularity.json.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DogeKingC/SWG/internal/popularity"
	"github.com/DogeKingC/SWG/internal/sources"
	"github.com/DogeKingC/SWG/internal/workshop"
)

var (
	dir        = flag.String("dir", "popularity-data", "data directory")
	tmTopPages = flag.Int("tm-top-pages", 100, "top-mods most-downloaded pages to read (10 items each)")
	tmNewPages = flag.Int("tm-new-pages", 20, "top-mods newest pages to read")
	tmDetails  = flag.Int("tm-details", 1500, "most top-mods item pages to read per run (for Workshop IDs)")
)

// tmInfo is what an item page told us, kept between runs.
type tmInfo struct {
	WS      string `json:"ws"`
	Author  string `json:"author,omitempty"`
	Version string `json:"version,omitempty"`
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "popindex:", err)
		os.Exit(1)
	}
}

func run() error {
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	snapDir := filepath.Join(*dir, "snapshots")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return err
	}
	snap := popularity.Snapshot{Date: today, Counts: map[string]int{}}
	items := map[string]*popularity.Item{}
	add := func(it popularity.Item) {
		if old := items[it.Ref]; old != nil && old.N >= it.N {
			return // the same Workshop item listed twice on top-mods
		}
		items[it.Ref] = &it
		snap.Counts[it.Ref] = it.N
	}

	if err := collectGB(add); err != nil {
		fmt.Fprintln(os.Stderr, "GameBanana:", err)
	} else {
		snap.Sources = append(snap.Sources, "gb")
	}
	if err := collectTW(add); err != nil {
		fmt.Fprintln(os.Stderr, "True Workshop:", err)
	} else {
		snap.Sources = append(snap.Sources, "tw")
	}
	if err := collectOW(add); err != nil {
		fmt.Fprintln(os.Stderr, "Open Workshop:", err)
	} else {
		snap.Sources = append(snap.Sources, "ow")
	}
	if err := collectNX(add); err != nil {
		fmt.Fprintln(os.Stderr, "Nexus Mods:", err)
	} else {
		snap.Sources = append(snap.Sources, "nx")
	}
	if err := collect01(add); err != nil {
		fmt.Fprintln(os.Stderr, "01 STUDIO:", err)
	} else {
		snap.Sources = append(snap.Sources, "s01")
	}
	if err := collectTM(add); err != nil {
		fmt.Fprintln(os.Stderr, "top-mods:", err)
	} else {
		snap.Sources = append(snap.Sources, "tm")
	}
	if len(snap.Sources) == 0 {
		return fmt.Errorf("no source answered")
	}
	if err := writeJSON(filepath.Join(snapDir, today+".json"), snap); err != nil {
		return err
	}

	// Keep Retention days; read them oldest first.
	snaps, err := loadSnapshots(snapDir, now)
	if err != nil {
		return err
	}
	list := make([]popularity.Item, 0, len(items))
	for _, it := range items {
		list = append(list, *it)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Ref < list[j].Ref })
	popularity.Compute(list, snaps)
	ix := popularity.Index{Generated: now, Since: snaps[0].Date, Metric: popularity.Metric, Items: list}
	fmt.Fprintf(os.Stderr, "%d items, sources %v, history since %s\n", len(list), snap.Sources, ix.Since)
	return writeJSON(filepath.Join(*dir, "popularity.json"), ix)
}

func collectGB(add func(popularity.Item)) error {
	all, err := sources.GBAll()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return fmt.Errorf("no mods listed")
	}
	// What each upload really is, from its archive's file list (categories
	// don't say: contraptions are filed under Vehicles, Building...).
	kinds := sources.GBKinds(all)
	for _, m := range all {
		kind, checked := kinds[m.ID], true
		if kind == "" {
			kind, checked = "mod", false
			if m.Category.Name == "Contraptions" {
				kind = "contraption"
			}
		}
		add(popularity.Item{Ref: fmt.Sprintf("gb:%d", m.ID), Src: "gb", Name: m.Name, Author: m.Submitter.Name, Image: m.Thumb(),
			Kind: kind, KindChecked: checked, Category: m.Category.Name, Date: time.Unix(m.Modified, 0).UTC().Format("2006-01-02"),
			URL: m.URL, N: m.Views})
	}
	return nil
}

func collectTW(add func(popularity.Item)) error {
	all, err := sources.TWAll()
	if err != nil {
		return err
	}
	for _, it := range all {
		kind := it.Type
		if kind == "" {
			kind = "mod"
		}
		add(popularity.Item{Ref: fmt.Sprintf("tw:%d", it.ID), Src: "tw", Name: it.Title, Author: it.Author, Image: it.Thumb(),
			Kind: kind, Date: it.CreatedTime().Format("2006-01-02"), URL: it.Page(), Reviewed: it.Reviewed(), N: it.Downloads})
	}
	return nil
}

func collectOW(add func(popularity.Item)) error {
	ix, err := workshop.FetchIndex()
	if err != nil {
		if strings.Contains(err.Error(), "nothing published yet") {
			return nil
		}
		return err
	}
	for _, e := range ix.Entries {
		if !e.Withdrawn {
			add(popularity.Item{Ref: "ow:" + e.Slug, Src: "ow", Name: e.Name, Author: e.Author, Image: e.Image, Kind: e.Kind,
				KindChecked: true, Date: e.Published.Format("2006-01-02"), URL: workshop.Page(e.Slug), Reviewed: true, N: e.Downloads})
		}
	}
	return nil
}

func collectNX(add func(popularity.Item)) error {
	all, err := sources.NXAll()
	if err != nil {
		return err
	}
	infos := sources.NXInfos(all)
	for _, m := range all {
		kind, checked := infos[m.ID].Kind, true
		if kind == "" {
			kind, checked = "mod", false
		}
		add(popularity.Item{Ref: fmt.Sprintf("nx:%d", m.ID), Src: "nx", Name: m.Name, Author: m.Author, Image: m.Thumbnail,
			Kind: kind, KindChecked: checked, Date: m.UpdatedTime().Format("2006-01-02"), URL: m.Page(), N: m.Downloads})
	}
	return nil
}

func collect01(add func(popularity.Item)) error {
	all, err := sources.S01All()
	if err != nil {
		return err
	}
	for _, m := range all {
		if ws := m.WorkshopID(); ws != "" {
			// Ref "s01:" keeps its counter apart from top-mods' views of the
			// same Workshop item; the window turns it back into sky:<id>.
			add(popularity.Item{Ref: "s01:" + ws, Src: "s01", Name: m.Title, Author: "01 STUDIO", Image: m.Image(),
				Kind: "mod", KindChecked: true, Category: m.Category, Date: m.CreatedTime().Format("2006-01-02"), URL: m.Page(), N: m.Views})
		}
	}
	return nil
}

func collectTM(add func(popularity.Item)) error {
	seen := map[string]sources.TMSummary{}
	var order []string
	list := func(name string, pages int, f func(int) ([]sources.TMSummary, error)) error {
		for p := 1; p <= pages; p++ {
			l, err := f(p)
			if err != nil {
				if p == 1 {
					return err
				}
				fmt.Fprintf(os.Stderr, "top-mods %s page %d: %v\n", name, p, err)
				break
			}
			if len(l) == 0 {
				break
			}
			for _, s := range l {
				if _, ok := seen[s.ID]; !ok {
					order = append(order, s.ID)
				}
				seen[s.ID] = s
			}
			time.Sleep(300 * time.Millisecond) // be gentle
		}
		return nil
	}
	if err := list("top", *tmTopPages, sources.TMTop); err != nil {
		return err
	}
	if err := list("newest", *tmNewPages, sources.TMLatest); err != nil {
		return err
	}

	// Workshop IDs come from item pages; remember them between runs.
	infoPath := filepath.Join(*dir, "topmods.json")
	info := map[string]tmInfo{}
	if b, err := os.ReadFile(infoPath); err == nil {
		json.Unmarshal(b, &info)
	}
	var missing []string
	for _, id := range order {
		if _, ok := info[id]; !ok && len(missing) < *tmDetails {
			missing = append(missing, id)
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, id := range missing {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			it, err := sources.TMDetails(seen[id].URL)
			if err != nil {
				return
			}
			mu.Lock()
			info[id] = tmInfo{WS: it.WorkshopID, Author: it.Author, Version: it.Version}
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	if err := writeJSON(infoPath, info); err != nil {
		return err
	}
	for _, id := range order {
		s, in := seen[id], info[id]
		if in.WS == "" {
			continue // not a Workshop mirror (or its page could not be read)
		}
		add(popularity.Item{Ref: "sky:" + in.WS, Src: "tm", Name: strings.TrimSpace(s.Title), Author: in.Author, Image: s.Image,
			Kind: "mod", Date: ymd(in.Version), URL: s.URL, N: s.Views})
	}
	return nil
}

// ymd turns top-mods' 19.09.2026 into 2026-09-19.
func ymd(v string) string {
	if t := sources.ParseTMVersion(v); !t.IsZero() {
		return t.Format("2006-01-02")
	}
	return v
}

func loadSnapshots(snapDir string, now time.Time) ([]popularity.Snapshot, error) {
	ents, err := os.ReadDir(snapDir)
	if err != nil {
		return nil, err
	}
	cutoff := now.AddDate(0, 0, -popularity.Retention).Format("2006-01-02")
	var out []popularity.Snapshot
	for _, e := range ents {
		name := strings.TrimSuffix(e.Name(), ".json")
		if name < cutoff {
			os.Remove(filepath.Join(snapDir, e.Name())) // older than the retention window
			continue
		}
		b, err := os.ReadFile(filepath.Join(snapDir, e.Name()))
		if err != nil {
			return nil, err
		}
		var s popularity.Snapshot
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, nil
}

func writeJSON(p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}
