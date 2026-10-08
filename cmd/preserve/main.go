// preserve builds the pre-worm archive (see package preserve), a batch per
// run, from a scheduled GitHub Action (.github/workflows/archive.yml):
//
//	preserve -data <checkout of archive-data> -optout archive/optout.txt
//
// Each run reads more of the Skymods catalogue and of top-mods' list,
// downloads the pre-worm copies it hasn't recorded yet, and records their
// SHA-256, size, contents and scan result in archive.json. Files are only
// kept when Internet Archive keys are configured (IA_ACCESS, IA_SECRET,
// IA_ITEM): they go to that item, except opted-out mods.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Trlydev/SWG/internal/app"
	"github.com/Trlydev/SWG/internal/archive"
	"github.com/Trlydev/SWG/internal/manager"
	"github.com/Trlydev/SWG/internal/preserve"
	"github.com/Trlydev/SWG/internal/scan"
	"github.com/Trlydev/SWG/internal/sources"
)

var (
	dataDir   = flag.String("data", "archive-data", "checkout of the archive-data branch")
	optoutF   = flag.String("optout", "archive/optout.txt", "opt-out list")
	skyPages  = flag.Int("sky-pages", 30, "Skymods catalogue pages to read per run")
	skyDelay  = flag.Duration("sky-delay", 5*time.Second, "pause between Skymods catalogue pages (rapid pages from this IP trip Cloudflare's browser check, which then blocks the site for every ppgmods user for hours)")
	tmItems   = flag.Int("tm-items", 150, "top-mods items to read per run")
	maxDL     = flag.Int("max-downloads", 300, "most files to download per run")
	maxMinute = flag.Int("max-minutes", 300, "stop starting downloads after this long")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "preserve:", err)
		os.Exit(1)
	}
}

type candidate struct {
	mr     app.Mirror
	ws     string
	author string
	rev    time.Time
}

func run() error {
	start := time.Now()
	os.MkdirAll(*dataDir, 0o755)
	path := filepath.Join(*dataDir, "archive.json")
	arc := &preserve.Archive{Records: map[string]*preserve.Record{}}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, arc); err != nil {
			return fmt.Errorf("archive.json: %v", err)
		}
		if arc.Records == nil {
			arc.Records = map[string]*preserve.Record{}
		}
	}
	opt := preserve.LoadOptOut(*optoutF)
	for id, r := range arc.Records {
		if opt.Excludes(r.WorkshopID, r.Author) {
			delete(arc.Records, id) // opted out since: forget it
		}
	}
	home, _ := os.MkdirTemp("", "preserve-")
	defer os.RemoveAll(home)
	os.Setenv("PPGMODS_HOME", home) // downloads go to a throwaway cache
	a := &app.App{Opt: app.DefaultOptions(), Logf: func(f string, v ...any) { fmt.Fprintf(os.Stderr, f+"\n", v...) }}

	var cands []candidate
	// Skymods: catalogue pages, continuing where the last run stopped.
	for i := 0; i < *skyPages; i++ {
		page := arc.Cursor.SkyPage + 1
		if i > 0 {
			time.Sleep(*skyDelay)
		}
		items, err := sources.SkyLatest(page)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Skymods page %d: %v\n", page, err)
			break
		}
		if len(items) == 0 {
			arc.Cursor.SkyPage = 0 // end of the catalogue: start over next time
			break
		}
		arc.Cursor.SkyPage = page
		for _, it := range items {
			cands = append(cands, candidate{app.SkyMirror(it), it.WorkshopID, it.Author, it.Revision})
		}
	}
	// top-mods: its sitemap lists every People Playground item.
	if idx, err := sources.TMIndex(); err == nil && len(idx) > 0 {
		for i := 0; i < *tmItems; i++ {
			if arc.Cursor.TMOffset >= len(idx) {
				arc.Cursor.TMOffset = 0
			}
			sm := idx[arc.Cursor.TMOffset]
			arc.Cursor.TMOffset++
			if r := arc.Records["topmods:"+sm.ID]; r != nil && r.SHA256 != "" {
				continue
			}
			it, err := sources.TMDetails(sm.URL)
			if err != nil || it.WorkshopID == "" {
				continue
			}
			cands = append(cands, candidate{app.TMMirror(it), it.WorkshopID, it.Author, it.VersionTime})
			time.Sleep(200 * time.Millisecond)
		}
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "top-mods: %v\n", err)
	}

	store := newStore()
	downloads, recorded := 0, 0
	for _, c := range cands {
		if opt.Excludes(c.ws, c.author) {
			continue
		}
		old := arc.Records[c.mr.ID]
		if old != nil && (old.SHA256 != "" || !old.PreWorm || old.Gone && time.Since(old.Archived) < 30*24*time.Hour) {
			continue // done, not pre-worm, or gone recently (try again in a month)
		}
		rec := &preserve.Record{Mirror: c.mr.ID, WorkshopID: c.ws, Title: c.mr.Title, Author: c.author, Revision: c.rev,
			PreWorm: !c.rev.IsZero() && c.rev.Before(manager.WormCutoff), Archived: time.Now().UTC()}
		arc.Records[c.mr.ID] = rec
		recorded++
		if !rec.PreWorm {
			continue // revised on/after the worm started (or undated): recorded, not downloaded
		}
		if downloads >= *maxDL || time.Since(start) > time.Duration(*maxMinute)*time.Minute {
			rec.Error = "not downloaded yet"
			continue
		}
		downloads++
		file, err := a.DownloadCopy(c.mr, c.ws)
		if err != nil {
			rec.Error = err.Error()
			rec.Gone = strings.Contains(err.Error(), "gone") || strings.Contains(err.Error(), "No file") || strings.Contains(err.Error(), "no download")
			continue
		}
		rec.Error = ""
		if err := inspect(rec, file); err != nil {
			rec.Error = err.Error()
		}
		if store != nil && rec.SHA256 != "" {
			if u, err := store.put(rec, file); err == nil {
				rec.Stored = u
			} else {
				fmt.Fprintf(os.Stderr, "store %s: %v\n", rec.Mirror, err)
			}
		}
		os.Remove(file)
	}
	arc.Updated = time.Now().UTC()
	b, err := json.Marshal(arc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	total, pre, hashed, stored := arc.Stats()
	summary := fmt.Sprintf("## Pre-worm archive\n\nThis run: %d copies recorded, %d downloaded.\n\nArchive: %d copies, %d from before the worm, %d hashed and scanned, %d files stored.\n",
		recorded, downloads, total, pre, hashed, stored)
	fmt.Print(summary)
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		os.WriteFile(p, []byte(summary), 0o644)
	}
	return nil
}

// inspect fills in a record from the downloaded file.
func inspect(rec *preserve.Record, file string) error {
	sum, err := manager.FileSHA(file)
	if err != nil {
		return err
	}
	st, _ := os.Stat(file)
	rec.SHA256, rec.Size = sum, st.Size()
	dir, err := os.MkdirTemp("", "preserve-x-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := archive.Extract(file, dir); err != nil {
		rec.ScanMax = "unreadable"
		return fmt.Errorf("unpack: %v", err)
	}
	scan.PruneBuildOutput(dir)
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		switch {
		case strings.EqualFold(d.Name(), "mod.json") && rec.Kind != "mod":
			rec.Kind = "mod"
			var mj struct {
				ModVersion         string
				CreatorUGCIdentity json.RawMessage
			}
			if b, err := os.ReadFile(p); err == nil && json.Unmarshal([]byte(strings.TrimPrefix(string(b), "\ufeff")), &mj) == nil {
				rec.ModVersion, rec.UGC = strings.TrimSpace(mj.ModVersion), manager.UGCString(mj.CreatorUGCIdentity)
			}
		case strings.EqualFold(filepath.Ext(p), ".jaap") && rec.Kind == "":
			rec.Kind = "contraption"
		}
		return nil
	})
	rep, err := scan.Dir(dir)
	if err != nil {
		return err
	}
	rec.ScanMax = "none"
	if rep.Max() >= 0 {
		rec.ScanMax = rep.Max().String()
	}
	seen := map[string]bool{}
	for _, f := range rep.Findings {
		if f.Severity >= scan.Medium && !seen[f.Rule] {
			seen[f.Rule] = true
			rec.Rules = append(rec.Rules, f.Rule)
		}
	}
	sort.Strings(rec.Rules)
	return nil
}

// store keeps files on the Internet Archive (its S3-like API), when keys
// are configured.
type iaStore struct{ access, secret, item string }

func newStore() *iaStore {
	s := &iaStore{os.Getenv("IA_ACCESS"), os.Getenv("IA_SECRET"), os.Getenv("IA_ITEM")}
	if s.access == "" || s.secret == "" || s.item == "" {
		return nil
	}
	return s
}

func (s *iaStore) put(rec *preserve.Record, file string) (string, error) {
	name := fmt.Sprintf("%s/%s-%s%s", rec.WorkshopID, strings.ReplaceAll(rec.Mirror, ":", "-"), rec.SHA256[:12], filepath.Ext(file))
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, _ := f.Stat()
	req, err := http.NewRequest("PUT", "https://s3.us.archive.org/"+s.item+"/"+name, f)
	if err != nil {
		return "", err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Authorization", "LOW "+s.access+":"+s.secret)
	req.Header.Set("x-archive-auto-make-bucket", "1")
	req.Header.Set("x-archive-meta-mediatype", "software")
	req.Header.Set("x-archive-meta-collection", "opensource_media")
	req.Header.Set("x-archive-meta-title", "People Playground Steam Workshop mods (pre-worm copies)")
	req.Header.Set("x-archive-meta-description", "Copies of People Playground Steam Workshop mods from before the September 2026 worm, recorded by PPG Mod Manager's pre-worm archive (https://github.com/Trlydev/SWG). Authors can opt out there.")
	resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("Internet Archive: HTTP %d", resp.StatusCode)
	}
	return "https://archive.org/download/" + s.item + "/" + name, nil
}
