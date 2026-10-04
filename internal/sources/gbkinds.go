package sources

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GameBanana's categories don't say whether an upload is a mod or a
// contraption: authors file contraptions under Vehicles, Military or
// Building. What the archive holds does: a mod has a mod.json, a contraption
// a .jaap file. GameBanana's legacy API lists every file inside an uploaded
// archive, for many items in one request.

const gbLegacy = "https://api.gamebanana.com/Core/Item/Data"

// Kinds of GameBanana uploads.
const (
	KindMod         = "mod"
	KindContraption = "contraption"
	KindOther       = "other" // neither (skins, textures, saves): not installable
)

// ClassifyFiles decides what an archive is from its file names: "mod",
// "contraption", "other", or "" when it can't tell (nested archives).
func ClassifyFiles(names []string) string {
	var mod, jaap, nested bool
	for _, n := range names {
		base := strings.ToLower(path.Base(strings.ReplaceAll(n, `\`, "/")))
		switch {
		case base == "mod.json":
			mod = true
		case strings.HasSuffix(base, ".jaap"):
			jaap = true
		case strings.HasSuffix(base, ".zip") || strings.HasSuffix(base, ".rar") || strings.HasSuffix(base, ".7z"):
			nested = true
		}
	}
	switch {
	case mod:
		return KindMod
	case jaap:
		return KindContraption
	case nested || len(names) == 0:
		return ""
	}
	return KindOther
}

type gbKind struct {
	Kind     string    `json:"kind"`
	File     int       `json:"file,omitempty"`
	Modified int64     `json:"modified,omitempty"` // the mod's _tsDateModified when classified; -1: from the published index
	At       time.Time `json:"at"`
}

var (
	gbKindMu     sync.Mutex
	gbKinds      map[int]gbKind
	gbKindsDirty bool
)

func gbKindPath() string {
	if IndexCacheDir == "" {
		return ""
	}
	return filepath.Join(IndexCacheDir, "gamebanana-kinds.json")
}

// loadGBKinds reads the disk cache; call with gbKindMu held.
func loadGBKinds() {
	if gbKinds != nil {
		return
	}
	gbKinds = map[int]gbKind{}
	if p := gbKindPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			json.Unmarshal(b, &gbKinds)
		}
	}
}

func saveGBKinds() {
	gbKindMu.Lock()
	defer gbKindMu.Unlock()
	if !gbKindsDirty {
		return
	}
	if p := gbKindPath(); p != "" {
		if b, err := json.Marshal(gbKinds); err == nil {
			os.MkdirAll(filepath.Dir(p), 0o755)
			if os.WriteFile(p, b, 0o644) == nil {
				gbKindsDirty = false
			}
		}
	}
}

// SeedGBKinds adds kinds classified elsewhere (the daily popularity index)
// for mods not classified on this computer yet.
func SeedGBKinds(kinds map[int]string) {
	gbKindMu.Lock()
	defer gbKindMu.Unlock()
	loadGBKinds()
	for id, k := range kinds {
		if _, ok := gbKinds[id]; !ok && k != "" {
			gbKinds[id] = gbKind{Kind: k, Modified: -1, At: time.Now()}
			gbKindsDirty = true
		}
	}
}

// GBKindOf returns what is known about a mod without asking GameBanana.
func GBKindOf(id int) string {
	gbKindMu.Lock()
	defer gbKindMu.Unlock()
	loadGBKinds()
	return gbKinds[id].Kind
}

// GBKinds classifies mods by their newest file's contents, asking
// GameBanana only about mods not classified yet or changed since.
func GBKinds(mods []GBMod) map[int]string {
	out := map[int]string{}
	var todo []GBMod
	gbKindMu.Lock()
	loadGBKinds()
	for _, m := range mods {
		k, ok := gbKinds[m.ID]
		fresh := ok && (k.Modified == -1 && time.Since(k.At) < 14*24*time.Hour || k.Modified >= m.Modified)
		if fresh {
			out[m.ID] = k.Kind
		} else {
			todo = append(todo, m)
		}
	}
	gbKindMu.Unlock()
	if len(todo) == 0 {
		return out
	}
	// Two batched requests per 50 mods: their files, then those files' contents.
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 4)
	for start := 0; start < len(todo); start += 50 {
		chunk := todo[start:min(start+50, len(todo))]
		wg.Add(1)
		go func(chunk []GBMod) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			files, err := gbNewestFiles(chunk)
			if err != nil {
				return
			}
			var fids []int
			for _, f := range files {
				fids = append(fids, f)
			}
			lists, err := gbArchiveLists(fids)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			gbKindMu.Lock()
			defer gbKindMu.Unlock()
			for _, m := range chunk {
				f, ok := files[m.ID]
				if !ok {
					continue
				}
				names, ok := lists[f]
				if !ok {
					continue
				}
				k := ClassifyFiles(names)
				out[m.ID] = k
				gbKinds[m.ID] = gbKind{Kind: k, File: f, Modified: m.Modified, At: time.Now()}
				gbKindsDirty = true
			}
		}(chunk)
	}
	wg.Wait()
	saveGBKinds()
	return out
}

func gbLegacyBatch(itemtype string, ids []int, field string, v any) error {
	q := url.Values{}
	for _, id := range ids {
		q.Add("itemtype[]", itemtype)
		q.Add("itemid[]", fmt.Sprint(id))
		q.Add("fields[]", field)
	}
	return getJSON(gbLegacy+"?"+q.Encode(), v)
}

// gbNewestFiles maps each mod to its newest file.
func gbNewestFiles(mods []GBMod) (map[int]int, error) {
	ids := make([]int, len(mods))
	for i, m := range mods {
		ids[i] = m.ID
	}
	var rows []json.RawMessage
	if err := gbLegacyBatch("Mod", ids, "Files().aFiles()", &rows); err != nil {
		return nil, err
	}
	out := map[int]int{}
	for i, row := range rows {
		if i >= len(ids) {
			break
		}
		var cell []map[string]struct {
			ID    int   `json:"_idRow"`
			Added int64 `json:"_tsDateAdded"`
		}
		if json.Unmarshal(row, &cell) != nil || len(cell) == 0 {
			continue // no files, or an error for this item
		}
		var best, bestAt int64 = 0, -1
		for _, f := range cell[0] {
			if f.Added > bestAt {
				best, bestAt = int64(f.ID), f.Added
			}
		}
		if best != 0 {
			out[ids[i]] = int(best)
		}
	}
	return out, nil
}

// gbArchiveLists returns the file names inside each archive.
func gbArchiveLists(fileIDs []int) (map[int][]string, error) {
	var rows []json.RawMessage
	if err := gbLegacyBatch("File", fileIDs, "Metadata().aArchiveFilesList()", &rows); err != nil {
		return nil, err
	}
	out := map[int][]string{}
	for i, row := range rows {
		if i >= len(fileIDs) {
			break
		}
		var cell [][]string
		if json.Unmarshal(row, &cell) != nil || len(cell) == 0 {
			continue
		}
		out[fileIDs[i]] = cell[0]
	}
	return out, nil
}

var (
	gbAllMu sync.Mutex
	gbAll   []GBMod
	gbAllAt time.Time
)

// GBAll lists every People Playground upload (about 560), newest change
// first, cached for 30 minutes.
func GBAll() ([]GBMod, error) {
	gbAllMu.Lock()
	defer gbAllMu.Unlock()
	if gbAll != nil && time.Since(gbAllAt) < 30*time.Minute {
		return gbAll, nil
	}
	var all []GBMod
	for page := 1; page <= 40; page++ {
		mods, err := GBList("", 0, "Generic_LatestModified", page, 50)
		if err != nil {
			if all != nil {
				break
			}
			return nil, err
		}
		all = append(all, mods...)
		if len(mods) < 50 {
			break
		}
	}
	gbAll, gbAllAt = all, time.Now()
	return all, nil
}
