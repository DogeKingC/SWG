package sources

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Nexus Mods (https://www.nexusmods.com/peopleplayground) has a People
// Playground section. Its GraphQL API lists mods without an account; files
// are downloaded on the website while signed in (its v1 download API needs a
// personal key and, for direct links, a paid account), so ppgmods opens the
// files page in the browser and imports what lands in Downloads.

const (
	nxGame = "peopleplayground"
	nxSite = "https://www.nexusmods.com/peopleplayground/mods/"
)

// nxGraphQL is Nexus's public API (a variable so tests can point it elsewhere).
var nxGraphQL = "https://api.nexusmods.com/v2/graphql"

// NXMod is one Nexus Mods entry.
type NXMod struct {
	ID           int    `json:"modId"`
	Name         string `json:"name"`
	Summary      string `json:"summary"`
	Description  string `json:"description,omitempty"`
	Author       string `json:"author"`
	Uploader     string `json:"-"`
	Version      string `json:"version"`
	Downloads    int    `json:"downloads"`
	Endorsements int    `json:"endorsements"`
	Created      string `json:"createdAt"`
	Updated      string `json:"updatedAt"`
	Picture      string `json:"pictureUrl"`
	Thumbnail    string `json:"thumbnailUrl"`
	Adult        bool   `json:"adultContent"`
	Status       string `json:"status"`
}

// Page is the mod's page; FilesPage its download tab.
func (m NXMod) Page() string      { return fmt.Sprintf("%s%d", nxSite, m.ID) }
func (m NXMod) FilesPage() string { return m.Page() + "?tab=files" }

// UpdatedTime is the last change on Nexus.
func (m NXMod) UpdatedTime() time.Time {
	t, _ := time.Parse(time.RFC3339, m.Updated)
	if t.IsZero() {
		t, _ = time.Parse(time.RFC3339, m.Created)
	}
	return t
}

var (
	nxMu  sync.Mutex
	nxAll []NXMod
	nxAt  time.Time
)

// NXAll returns every published, non-adult People Playground mod on Nexus,
// cached for an hour.
func NXAll() ([]NXMod, error) {
	nxMu.Lock()
	defer nxMu.Unlock()
	if nxAll != nil && time.Since(nxAt) < time.Hour {
		return nxAll, nil
	}
	const q = `query($offset: Int, $count: Int) { mods(filter: {gameDomainName: {value: "` + nxGame + `", op: EQUALS}}, offset: $offset, count: $count) {
		totalCount nodes { modId name summary author uploader { name } version downloads endorsements createdAt updatedAt pictureUrl thumbnailUrl adultContent status } } }`
	var all []NXMod
	done := false
	// The API returns at most 80 per page whatever count asks for: page by
	// what actually arrived.
	for offset := 0; offset < 5000 && !done; {
		var out struct {
			Data struct {
				Mods struct {
					Total int `json:"totalCount"`
					Nodes []struct {
						NXMod
						Uploader struct {
							Name string `json:"name"`
						} `json:"uploader"`
					} `json:"nodes"`
				} `json:"mods"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := nxQuery(q, map[string]any{"offset": offset, "count": 100}, &out); err != nil {
			if all != nil {
				done = true // a partial listing is not a cap hit
				break
			}
			return nil, err
		}
		if len(out.Errors) > 0 {
			return nil, fmt.Errorf("Nexus Mods: %s", out.Errors[0].Message)
		}
		for _, n := range out.Data.Mods.Nodes {
			m := n.NXMod
			m.Uploader = n.Uploader.Name
			if m.Status == "published" && !m.Adult {
				all = append(all, m)
			}
		}
		offset += len(out.Data.Mods.Nodes)
		if offset >= out.Data.Mods.Total || len(out.Data.Mods.Nodes) == 0 {
			done = true
		}
	}
	if !done {
		Warn("Nexus Mods lists more than 5000 uploads; the rest is not shown")
	}
	nxAll, nxAt = all, time.Now()
	return all, nil
}

// NXGet returns one mod from the catalogue.
func NXGet(id int) (*NXMod, error) {
	all, err := NXAll()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("Nexus Mods has no People Playground mod %d", id)
}

func nxQuery(query string, vars map[string]any, v any) error {
	b, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequest("POST", nxGraphQL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Nexus Mods: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}

// NXInfo is what a Nexus upload's newest main file holds, from Nexus's
// public content preview: mod or contraption, and the Workshop ID when its
// folder is named like a Steam download (3576213306_jujutsu_playground).
type NXInfo struct {
	Kind       string    `json:"kind"`
	WorkshopID string    `json:"ws,omitempty"`
	File       string    `json:"file"`
	At         time.Time `json:"at"`
}

const nxGameID = 4138

var (
	nxInfoMu sync.Mutex
	nxInfos  map[int]NXInfo
)

func nxInfoPath() string {
	if IndexCacheDir == "" {
		return ""
	}
	return filepath.Join(IndexCacheDir, "nexus-files.json")
}

// NXInfos classifies Nexus uploads. A file's contents never change (a new
// upload is a new file), so each file's preview is read once and cached.
func NXInfos(mods []NXMod) map[int]NXInfo {
	files := nxMainFiles(mods)
	nxInfoMu.Lock()
	if nxInfos == nil {
		nxInfos = map[int]NXInfo{}
		if p := nxInfoPath(); p != "" {
			if b, err := os.ReadFile(p); err == nil {
				json.Unmarshal(b, &nxInfos)
			}
		}
	}
	out := map[int]NXInfo{}
	var todo []int
	for _, m := range mods {
		f, ok := files[m.ID]
		if !ok {
			if in, ok := nxInfos[m.ID]; ok {
				out[m.ID] = in // file list unavailable: keep what we knew
			}
			continue
		}
		if in, ok := nxInfos[m.ID]; ok && in.File == f {
			out[m.ID] = in
		} else {
			todo = append(todo, m.ID)
		}
	}
	nxInfoMu.Unlock()
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 4)
	for _, id := range todo {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			names, err := nxPreview(id, files[id])
			if err != nil {
				return
			}
			in := NXInfo{Kind: ClassifyFiles(names), File: files[id], At: time.Now()}
			for _, n := range names {
				top := strings.SplitN(strings.ReplaceAll(n, `\`, "/"), "/", 2)[0]
				if m := reNXWorkshopDir.FindStringSubmatch(top); m != nil {
					in.WorkshopID = m[1]
					break
				}
			}
			mu.Lock()
			out[id] = in
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	nxInfoMu.Lock()
	for id, in := range out {
		nxInfos[id] = in
	}
	if p := nxInfoPath(); p != "" && len(todo) > 0 {
		if b, err := json.Marshal(nxInfos); err == nil {
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, b, 0o644)
		}
	}
	nxInfoMu.Unlock()
	return out
}

var reNXWorkshopDir = regexp.MustCompile(`^(\d{8,12})_`)

// nxMainFiles returns each mod's newest main file (its uri), asking about
// 25 mods per GraphQL request.
var (
	nxFilesMu sync.Mutex
	nxFiles   = map[int]nxFileEntry{}
)

type nxFileEntry struct {
	uri string
	at  time.Time
}

func nxMainFiles(mods []NXMod) map[int]string {
	out := map[int]string{}
	var ask []NXMod
	nxFilesMu.Lock()
	for _, m := range mods {
		if e, ok := nxFiles[m.ID]; ok && time.Since(e.at) < time.Hour {
			if e.uri != "" {
				out[m.ID] = e.uri
			}
		} else {
			ask = append(ask, m)
		}
	}
	nxFilesMu.Unlock()
	fresh := nxAskFiles(ask)
	nxFilesMu.Lock()
	for _, m := range ask {
		if u, ok := fresh[m.ID]; ok {
			nxFiles[m.ID] = nxFileEntry{u, time.Now()}
			out[m.ID] = u
		}
	}
	nxFilesMu.Unlock()
	return out
}

func nxAskFiles(mods []NXMod) map[int]string {
	out := map[int]string{}
	for start := 0; start < len(mods); start += 25 {
		chunk := mods[start:min(start+25, len(mods))]
		var q strings.Builder
		q.WriteString("{")
		for _, m := range chunk {
			fmt.Fprintf(&q, " m%d: modFiles(modId: %d, gameId: %d) { uri category date }", m.ID, m.ID, nxGameID)
		}
		q.WriteString(" }")
		var res struct {
			Data map[string][]struct {
				URI      string `json:"uri"`
				Category string `json:"category"`
				Date     int64  `json:"date"`
			} `json:"data"`
		}
		if err := nxQuery(q.String(), nil, &res); err != nil {
			continue
		}
		for key, fs := range res.Data {
			var id int
			if _, err := fmt.Sscanf(key, "m%d", &id); err != nil {
				continue
			}
			best, bestRank, bestDate := "", -1, int64(-1)
			for _, f := range fs {
				rank := map[string]int{"MAIN": 3, "UPDATE": 2, "OPTIONAL": 1, "MISCELLANEOUS": 1}[f.Category]
				if f.Category == "ARCHIVED" || f.Category == "DELETED" {
					continue
				}
				if rank > bestRank || rank == bestRank && f.Date > bestDate {
					best, bestRank, bestDate = f.URI, rank, f.Date
				}
			}
			if best != "" {
				out[id] = best
			}
		}
	}
	return out
}

// nxPreview lists the files inside a Nexus upload (Nexus's public content
// preview).
func nxPreview(modID int, uri string) ([]string, error) {
	u := fmt.Sprintf("https://file-metadata.nexusmods.com/file/nexus-files-s3-meta/%d/%d/%s.json", nxGameID, modID, url.PathEscape(uri))
	var tree struct {
		Children []nxNode `json:"children"`
	}
	if err := getJSON(u, &tree); err != nil {
		return nil, err
	}
	var names []string
	var walk func([]nxNode)
	walk = func(ns []nxNode) {
		for _, n := range ns {
			if n.Type == "file" {
				names = append(names, n.Path)
			}
			walk(n.Children)
		}
	}
	walk(tree.Children)
	return names, nil
}

type nxNode struct {
	Path     string   `json:"path"`
	Type     string   `json:"type"`
	Children []nxNode `json:"children"`
}
