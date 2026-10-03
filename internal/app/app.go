// Package app implements ppgmods' operations (search, install, import,
// update, backup, restore) on top of the manager and sources packages. The
// command line and the GUI both drive it.
package app

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DogeKingC/SWG/internal/game"
	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/sources"
)

type Options struct {
	Game, Dest, WorkshopID, Name string
	Downloads                    string        // browser download folder to watch
	Wait                         time.Duration // how long to wait for a browser download
	Mirror                       string        // Workshop items: use this mirror copy ("skymods:<id>", "topmods:<id>") instead of choosing
	NoWatch, Yes, Offline        bool
	BrowserFallback              bool // on a modsbase refusal, open the browser and watch Downloads
	UpdateOnly                   bool // fetch: skip when the installed file is already the newest
	FileID                       int
	Revision                     time.Time // known revision date (from a backup manifest)
	Policy                       manager.Policy
}

func DefaultOptions() Options {
	return Options{Wait: 10 * time.Minute, Policy: manager.Policy{Cooldown: 48 * time.Hour}}
}

type App struct {
	Opt  Options
	Logf func(format string, a ...any)
}

func (a *App) logf(format string, args ...any) {
	if a.Logf != nil {
		a.Logf(format, args...)
	}
}

// with returns a copy of a with modified options.
func (a *App) with(f func(*Options)) *App {
	b := *a
	f(&b.Opt)
	return &b
}

// Manager loads state and, when needGame is set, locates the Mods folder and
// the blocklist.
func (a *App) Manager(needGame bool) (*manager.Manager, error) {
	st, err := manager.LoadState()
	if err != nil {
		return nil, err
	}
	m := &manager.Manager{State: st, Policy: a.Opt.Policy, Log: a.logf}
	if needGame {
		dir, err := game.FindGameDir(a.Opt.Game)
		if err != nil {
			return nil, err
		}
		m.ModsDir = game.ModsDir(dir)
		m.ContraptionsDir = game.ContraptionsDir(dir)
		m.Blocklist = manager.LoadBlocklist(!a.Opt.Offline, a.logf)
	}
	return m, nil
}

func FmtTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format("2006-01-02")
}

func HumanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ---- search ----

type SearchResult struct {
	Ref         string   `json:"ref"` // gb:<id> or sky:<workshop id>
	Source      string   `json:"source"`
	Name        string   `json:"name"`
	Author      string   `json:"author"`
	Category    string   `json:"category,omitempty"`
	Date        string   `json:"date"`
	Size        string   `json:"size,omitempty"`
	URL         string   `json:"url"`
	Image       string   `json:"image,omitempty"`
	AfterCutoff bool     `json:"after_cutoff,omitempty"` // Workshop: every known copy is after the cutoff
	Reviewed    bool     `json:"reviewed,omitempty"`     // True Workshop: reviewed by the site's maintainers
	Downloads   int      `json:"download_count,omitempty"`
	Kind        string   `json:"kind,omitempty"`    // "contraption" when known
	Mirrors     []string `json:"mirrors,omitempty"` // Workshop: "top-mods 19.09.2026", ...

	newest time.Time
}

type SearchResults struct {
	GameBanana []SearchResult `json:"gamebanana"`
	TrueWS     []SearchResult `json:"trueworkshop"` // True Workshop uploads (maintainer-reviewed archive)
	Workshop   []SearchResult `json:"workshop"`     // deleted Steam Workshop items, merged across mirrors
	Errors     []string       `json:"errors,omitempty"`
}

// Search queries GameBanana and both Workshop mirrors. Skymods and top-mods
// results for the same Workshop item are merged into one entry.
func Search(q string, page int) SearchResults {
	var r SearchResults
	var wg sync.WaitGroup
	var mu sync.Mutex
	addErr := func(e string) { mu.Lock(); r.Errors = append(r.Errors, e); mu.Unlock() }
	var sky []sources.SkyItem
	var tm []*sources.TMItem
	wg.Add(4)
	go func() {
		defer wg.Done()
		sort := "popular"
		if strings.TrimSpace(q) == "" {
			sort = "newest"
		}
		items, _, err := sources.TWSearch(q, sort, (page-1)*24, 24)
		if err != nil {
			addErr("True Workshop: " + err.Error())
			return
		}
		for _, it := range items {
			r.TrueWS = append(r.TrueWS, TWResult(it))
		}
	}()
	go func() {
		defer wg.Done()
		gb, err := sources.GBSearch(q, page)
		if err != nil {
			addErr("GameBanana: " + err.Error())
			return
		}
		for _, m := range gb {
			r.GameBanana = append(r.GameBanana, SearchResult{
				Ref: fmt.Sprintf("gb:%d", m.ID), Source: "GameBanana", Name: m.Name, Author: m.Submitter.Name,
				Category: m.Category.Name, Date: time.Unix(m.Modified, 0).Format("2006-01-02"), URL: m.URL, Image: m.Thumb(),
			})
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		if strings.TrimSpace(q) == "" {
			sky, err = sources.SkyLatest(page)
		} else {
			sky, err = sources.SkySearch(q, page)
		}
		if err != nil {
			addErr("Skymods: " + err.Error())
		}
	}()
	go func() {
		defer wg.Done()
		var list []sources.TMSummary
		var err error
		if strings.TrimSpace(q) == "" {
			list, err = sources.TMLatest(page)
		} else {
			list, err = sources.TMSearch(q, page)
		}
		if err != nil {
			addErr("top-mods: " + err.Error())
			return
		}
		var urls []string
		for i, s := range list {
			if i < 12 {
				urls = append(urls, s.URL)
			}
		}
		tm = tmDetailsAll(urls)
	}()
	wg.Wait()

	byWS := map[string]*SearchResult{}
	var order []string
	add := func(ws string, mr Mirror) {
		rememberTitle(ws, mr.Title, func() string {
			if mr.tm != nil {
				return mr.tm.URL
			}
			return ""
		}())
		e := byWS[ws]
		if e == nil {
			e = &SearchResult{Ref: "sky:" + ws, Source: "Steam Workshop", Name: mr.Title, Author: mr.Author,
				URL: "https://steamcommunity.com/sharedfiles/filedetails/?id=" + ws, AfterCutoff: true}
			byWS[ws] = e
			order = append(order, ws)
		}
		if mr.Source == "top-mods" && mr.Image != "" {
			e.Image = mr.Image // hosted by top-mods: still there after Valve's deletion
		} else if e.Image == "" {
			e.Image = mr.Image
		}
		if e.Author == "" {
			e.Author = mr.Author
		}
		e.Mirrors = append(e.Mirrors, mr.Source+" "+mr.Version)
		if !mr.AfterCutoff {
			e.AfterCutoff = false
		}
		if e.newest.IsZero() || mr.VersionTime.After(e.newest) {
			e.newest, e.Date, e.Size = mr.VersionTime, mr.Version, mr.Size
		}
	}
	for _, it := range sky {
		add(it.WorkshopID, skyMirror(it))
	}
	for _, it := range tm {
		if it.WorkshopID != "" {
			add(it.WorkshopID, tmMirror(it))
		}
	}
	for _, ws := range order {
		r.Workshop = append(r.Workshop, *byWS[ws])
	}
	return r
}

// ---- install ----

var reGBURL = regexp.MustCompile(`gamebanana\.com/mods/(\d+)`)
var reTWURL = regexp.MustCompile(`ppgworkshop\.onrender\.com/.*?(?:item-|id=)(\d+)`)
var reWSURL = regexp.MustCompile(`steamcommunity\.com/(?:sharedfiles|workshop)/filedetails/\?id=(\d+)`)

// NormalizeRef turns GameBanana / Steam Workshop / top-mods / True Workshop
// links and bare Workshop IDs into gb:<id> / sky:<workshop id> / tw:<id>.
func NormalizeRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if mm := reGBURL.FindStringSubmatch(ref); mm != nil {
		return "gb:" + mm[1]
	}
	if mm := reTWURL.FindStringSubmatch(ref); mm != nil {
		return "tw:" + mm[1]
	}
	if mm := reWSURL.FindStringSubmatch(ref); mm != nil {
		return "sky:" + mm[1]
	}
	if reWorkshopDir.MatchString(ref) {
		return "sky:" + ref
	}
	if strings.Contains(ref, "top-mods.com/mods/people-playground/") {
		if it, err := sources.TMDetails(ref); err == nil && it.WorkshopID != "" {
			rememberTitle(it.WorkshopID, it.Title, ref)
			return "sky:" + it.WorkshopID
		}
	}
	return ref
}

// NeedsBrowser is returned when modsbase.com will not hand out a download
// link to ppgmods (a Cloudflare check or a captcha). The user can open URL in
// a browser; ppgmods then picks the file up from the Downloads folder.
type NeedsBrowser struct {
	URL        string `json:"url"`
	Reason     string `json:"reason"`
	WorkshopID string `json:"workshop_id"`
}

func (e *NeedsBrowser) Error() string {
	return "modsbase.com did not give ppgmods a download link (" + e.Reason + ")"
}

// Install installs gb:<id>, sky:<workshop id>, a GameBanana URL or a Steam
// Workshop URL.
func (a *App) Install(m *manager.Manager, ref string) error {
	ref = NormalizeRef(ref)
	c, err := a.Fetch(m, ref, m.State.Mods[ref])
	var nb *NeedsBrowser
	if errors.As(err, &nb) && a.Opt.BrowserFallback {
		c, err = a.viaBrowser(nb)
	}
	if err != nil || c == nil {
		return err
	}
	return m.Install(c)
}

// Fetch downloads (or reuses the cached download of) a mod and returns it as
// an install candidate. prev is the installed version, if any; for GameBanana
// a nil candidate with nil error means prev is already the newest file.
func (a *App) Fetch(m *manager.Manager, ref string, prev *manager.Installed) (*manager.Candidate, error) {
	// One download per mod at a time: a preview and an install of the same
	// mod share the cached file.
	mu, _ := fetchLocks.LoadOrStore(ref, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	switch {
	case strings.HasPrefix(ref, "gb:"):
		id, err := strconv.Atoi(strings.TrimPrefix(ref, "gb:"))
		if err != nil {
			return nil, fmt.Errorf("bad GameBanana id %q", ref)
		}
		return a.fetchGB(id, a.Opt.FileID, prev)
	case strings.HasPrefix(ref, "sky:"):
		return a.fetchWorkshop(m, strings.TrimPrefix(ref, "sky:"))
	case strings.HasPrefix(ref, "tw:"):
		id, err := strconv.Atoi(strings.TrimPrefix(ref, "tw:"))
		if err != nil {
			return nil, fmt.Errorf("bad True Workshop id %q", ref)
		}
		return a.fetchTW(id, prev)
	}
	return nil, fmt.Errorf("unknown reference %q; use gb:<id>, sky:<workshop id>, tw:<id>, or a GameBanana/Steam Workshop link", ref)
}

var fetchLocks sync.Map

// CacheDir is where downloaded archives and their thumbnails are kept, so a
// preview and the install that follows download only once.
func CacheDir(key string) (string, error) {
	d, err := manager.ConfigDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(d, "cache", strings.ReplaceAll(key, ":", "-"))
	return p, os.MkdirAll(p, 0o755)
}

// PruneCache removes cached downloads older than maxAge.
func PruneCache(maxAge time.Duration) {
	d, err := manager.ConfigDir()
	if err != nil {
		return
	}
	ents, _ := os.ReadDir(filepath.Join(d, "cache"))
	for _, e := range ents {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
			os.RemoveAll(filepath.Join(d, "cache", e.Name()))
		}
	}
}

func (a *App) fetchGB(id, fileID int, prev *manager.Installed) (*manager.Candidate, error) {
	mod, err := sources.GBGetMod(id)
	if err != nil {
		return nil, err
	}
	files, err := sources.GBFiles(id)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("gb:%d has no files", id)
	}
	f := files[0]
	if fileID != 0 {
		found := false
		for _, x := range files {
			if x.ID == fileID {
				f, found = x, true
			}
		}
		if !found {
			return nil, fmt.Errorf("file %d not found in gb:%d", fileID, id)
		}
	}
	if a.Opt.UpdateOnly && prev != nil && prev.FileID == f.ID {
		return nil, nil
	}
	if !f.Clean() {
		return nil, &manager.Rejection{Reasons: []string{fmt.Sprintf("GameBanana malware analysis for %s is %q/%q/%q, not clean", f.Name, f.AVState, f.AVResult, f.Analysis)}}
	}
	dir, err := CacheDir(fmt.Sprintf("gb:%d", id))
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%d-%s", f.ID, filepath.Base(f.Name)))
	md5hex, _ := fileMD5(path)
	if md5hex == "" || !strings.EqualFold(md5hex, f.MD5) {
		a.logf("gb:%d %s - downloading %s (%s, uploaded %s)", id, mod.Name, f.Name, HumanSize(f.Size), f.AddedTime().Format("2006-01-02 15:04"))
		if md5hex, _, err = sources.Download(f.DownloadURL, path, 1<<30); err != nil {
			return nil, err
		}
	}
	if f.MD5 != "" && !strings.EqualFold(md5hex, f.MD5) {
		os.Remove(path)
		return nil, &manager.Rejection{Reasons: []string{fmt.Sprintf("checksum mismatch: GameBanana says %s, got %s", f.MD5, md5hex)}}
	}
	sha, err := manager.FileSHA(path)
	if err != nil {
		return nil, err
	}
	return &manager.Candidate{
		Key: fmt.Sprintf("gb:%d", id), Name: mod.Name, Source: mod.URL, Path: path,
		FileID: f.ID, Version: f.Version, Revision: f.AddedTime(), ArchiveSHA: sha,
	}, nil
}

func fileMD5(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Unavailable means no mirror still has the file.
type Unavailable struct{ Reason string }

func (e *Unavailable) Error() string { return e.Reason }

// viaBrowser opens the modsbase page in the browser and waits for the file
// to appear in the Downloads folder.
func (a *App) viaBrowser(nb *NeedsBrowser) (*manager.Candidate, error) {
	dl := a.Opt.Downloads
	if dl == "" {
		dl = DownloadFolder()
	}
	a.logf("Opening the download page in your browser: %s", nb.URL)
	a.logf("Click the real download button (ignore ads; never run an .exe).")
	if a.Opt.NoWatch || dl == "" {
		OpenBrowser(nb.URL)
		return nil, fmt.Errorf("download the file in your browser, then import it with workshop id %s", nb.WorkshopID)
	}
	since := time.Now()
	OpenBrowser(nb.URL)
	a.logf("Waiting for %s_* in %s (up to %s)...", nb.WorkshopID, dl, a.Opt.Wait)
	file, err := waitForDownload(dl, nb.WorkshopID, since, a.Opt.Wait)
	if err != nil {
		return nil, fmt.Errorf("%v; when you have the file, import it with workshop id %s", err, nb.WorkshopID)
	}
	a.logf("got %s", filepath.Base(file))
	return a.with(func(o *Options) { o.WorkshopID = nb.WorkshopID }).candidate(file)
}

// DownloadFolder returns the user's browser download folder.
func DownloadFolder() string {
	if runtime.GOOS != "windows" {
		if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil {
			if d := strings.TrimSpace(string(out)); d != "" {
				if st, err := os.Stat(d); err == nil && st.IsDir() {
					return d
				}
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	d := filepath.Join(home, "Downloads")
	if st, err := os.Stat(d); err == nil && st.IsDir() {
		return d
	}
	return ""
}

var partialExt = map[string]bool{".crdownload": true, ".part": true, ".partial": true, ".download": true, ".tmp": true}

// waitForDownload polls dir for an archive named "<workshop id>_..." (the
// naming modsbase uses) that appeared after since and has stopped growing.
func waitForDownload(dir, ws string, since time.Time, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	sizes := map[string]int64{}
	for time.Now().Before(deadline) {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			name := e.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if e.IsDir() || !strings.HasPrefix(name, ws) || partialExt[ext] {
				continue
			}
			if ext != ".zip" && ext != ".rar" && ext != ".7z" {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().Before(since.Add(-2*time.Second)) || info.Size() == 0 {
				continue
			}
			p := filepath.Join(dir, name)
			if prev, ok := sizes[p]; ok && prev == info.Size() {
				return p, nil
			}
			sizes[p] = info.Size()
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("no download for %s appeared in %s", ws, dir)
}

// OpenBrowser opens u in the default browser.
func OpenBrowser(u string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		c = exec.Command("open", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	return c.Start()
}

// OpenFolder shows a folder in the system file manager.
func OpenFolder(p string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer", p).Start()
	case "darwin":
		return exec.Command("open", p).Start()
	}
	return exec.Command("xdg-open", p).Start()
}

var reWorkshopDir = regexp.MustCompile(`^\d{6,12}$`)

// Import scans and installs an archive or folder already on disk.
func (a *App) Import(m *manager.Manager, p string) error {
	c, err := a.candidate(p)
	if err != nil {
		return err
	}
	return m.Install(c)
}

// candidate describes an archive or folder on disk as an install candidate.
func (a *App) candidate(p string) (*manager.Candidate, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	ws := a.Opt.WorkshopID
	if ws == "" && st.IsDir() && reWorkshopDir.MatchString(filepath.Base(abs)) {
		ws = filepath.Base(abs) // a folder copied from steamapps/workshop/content/1118200
	}
	c := &manager.Candidate{Path: abs, Name: a.Opt.Name}
	if !st.IsDir() {
		if c.ArchiveSHA, err = manager.FileSHA(abs); err != nil {
			return nil, err
		}
	}
	if ws != "" {
		c.Key, c.SteamOrig, c.Source = "sky:"+ws, true, "https://steamcommunity.com/sharedfiles/filedetails/?id="+ws
		if st.IsDir() {
			c.Revision = a.Opt.Revision
			if c.Revision.IsZero() {
				c.Revision = manager.NewestMtime(abs)
			}
			a.logf("using newest file time %s as the revision date", FmtTime(c.Revision))
		} else if !a.Opt.Revision.IsZero() {
			c.Revision = a.Opt.Revision
		} else if it, err := sources.SkyByWorkshopID(ws); err == nil && !it.Revision.IsZero() {
			c.Revision = it.Revision
			if c.Name == "" {
				c.Name = it.Title
			}
			a.logf("Skymods lists Workshop %s revision %s", ws, it.Revision.Format("2006-01-02 15:04"))
		}
	} else {
		if c.ArchiveSHA == "" {
			h, err := manager.TreeSHA(abs)
			if err != nil {
				return nil, err
			}
			c.ArchiveSHA = h
		}
		c.Key, c.Source = "local:"+c.ArchiveSHA[:12], abs
	}
	if c.Name == "" {
		c.Name = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	}
	return c, nil
}

// ---- update, restore, backup ----

type Summary struct {
	OK, Refused, Failed, Current int
}

// Update checks installed GameBanana mods for newer files. Without
// Opt.Yes it is a dry run.
func (a *App) Update(m *manager.Manager) Summary {
	var s Summary
	if !a.Opt.Yes {
		m.Policy.DryRun = true
		a.logf("dry run: checking only")
	}
	for _, inst := range m.State.Sorted() {
		if strings.HasPrefix(inst.Key, "tw:") {
			if inst.Pinned {
				a.logf("%s pinned, skipped", inst.Key)
				continue
			}
			id, _ := strconv.Atoi(strings.TrimPrefix(inst.Key, "tw:"))
			b := a.with(func(o *Options) { o.UpdateOnly = true })
			c, err := b.fetchTW(id, inst)
			if err == nil && c == nil {
				s.Current++
				continue
			}
			if err == nil {
				err = m.Install(c)
			}
			a.tally(&s, inst.Key, "HELD", err)
			continue
		}
		if !strings.HasPrefix(inst.Key, "gb:") {
			continue // Workshop copies are frozen: Steam no longer hosts them
		}
		if inst.Pinned {
			a.logf("%s pinned, skipped", inst.Key)
			continue
		}
		id, _ := strconv.Atoi(strings.TrimPrefix(inst.Key, "gb:"))
		files, err := sources.GBFiles(id)
		if err != nil {
			a.logf("%s: %v", inst.Key, err)
			s.Failed++
			continue
		}
		if len(files) == 0 || files[0].ID == inst.FileID {
			s.Current++
			continue
		}
		b := a.with(func(o *Options) { o.UpdateOnly, o.FileID = true, 0 })
		c, err := b.fetchGB(id, 0, inst)
		if err == nil && c != nil {
			err = m.Install(c)
		}
		a.tally(&s, inst.Key, "HELD", err)
	}
	verb := "updated"
	if m.Policy.DryRun {
		verb = "can update"
	}
	a.logf("%d up to date, %d %s, %d held back, %d errors", s.Current, s.OK, verb, s.Refused, s.Failed)
	return s
}

func (a *App) tally(s *Summary, ref, word string, err error) {
	var rej *manager.Rejection
	switch {
	case err == nil:
		s.OK++
	case errors.As(err, &rej):
		s.Refused++
		for _, r := range rej.Reasons {
			a.logf("  %s %s: %s", ref, word, r)
		}
	default:
		s.Failed++
		a.logf("  %s error: %v", ref, err)
	}
}

// InstallMany installs several references, continuing past failures.
func (a *App) InstallMany(m *manager.Manager, refs []string) Summary {
	var s Summary
	for _, ref := range refs {
		a.tally(&s, ref, "REFUSED", a.Install(m, ref))
	}
	return s
}

// Restore imports every item of a backup-workshop folder.
func (a *App) Restore(m *manager.Manager, dir string) (Summary, error) {
	var s Summary
	ents, err := os.ReadDir(dir)
	if err != nil {
		return s, err
	}
	dates := manager.ReadBackupDates(dir)
	if dates == nil {
		a.logf("warning: no manifest.json in %s; dating items by file times, which may be the copy time", dir)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b := a.with(func(o *Options) { o.WorkshopID, o.Revision = e.Name(), dates[e.Name()] })
		a.logf("== %s", e.Name())
		a.tally(&s, e.Name(), "REFUSED", b.Import(m, filepath.Join(dir, e.Name())))
	}
	a.logf("restore finished: %d installed, %d refused, %d errors", s.OK, s.Refused, s.Failed)
	return s, nil
}

// Backup copies the Steam Workshop cache into Opt.Dest (or a dated folder)
// and returns the folder used.
func (a *App) Backup() (string, error) {
	src := game.WorkshopDirs()
	if len(src) == 0 {
		return "", errors.New("no People Playground Workshop cache found (steamapps/workshop/content/1118200)")
	}
	dest := a.Opt.Dest
	if dest == "" {
		dir, err := manager.ConfigDir()
		if err != nil {
			return "", err
		}
		dest = filepath.Join(dir, "workshop-backup-"+time.Now().Format("20060102"))
	}
	a.logf("backing up %s -> %s", strings.Join(src, ", "), dest)
	items, err := manager.BackupWorkshop(src, dest, a.logf)
	if err != nil {
		return dest, err
	}
	after := 0
	for _, it := range items {
		if it.AfterCutoff {
			after++
		}
	}
	a.logf("backed up %d items (%d modified after the worm cutoff). Manifest: %s", len(items), after, filepath.Join(dest, "manifest.json"))
	return dest, nil
}

// Paths describes where ppgmods looks for things.
type Paths struct {
	Libraries    []string `json:"libraries"`
	Game         string   `json:"game"`
	Mods         string   `json:"mods"`
	Contraptions string   `json:"contraptions"`
	GameError    string   `json:"game_error,omitempty"`
	Workshop     []string `json:"workshop"`
	Data         string   `json:"data"`
	Downloads    string   `json:"downloads"`
}

func (a *App) Paths() Paths {
	p := Paths{Libraries: game.Libraries(), Workshop: game.WorkshopDirs(), Downloads: DownloadFolder()}
	if dir, err := game.FindGameDir(a.Opt.Game); err == nil {
		p.Game, p.Mods, p.Contraptions = dir, game.ModsDir(dir), game.ContraptionsDir(dir)
	} else {
		p.GameError = err.Error()
	}
	p.Data, _ = manager.ConfigDir()
	return p
}
