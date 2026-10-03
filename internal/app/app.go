// Package app implements ppgmods' operations (search, install, import,
// update, backup, restore) on top of the manager and sources packages. The
// command line and the GUI both drive it.
package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/DogeKingC/SWG/internal/game"
	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/sources"
)

type Options struct {
	Game, Dest, WorkshopID, Name string
	Downloads                    string        // browser download folder to watch
	Wait                         time.Duration // how long to wait for a browser download
	NoWatch, Yes, Offline        bool
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
	Ref         string `json:"ref"` // gb:<id> or sky:<workshop id>
	Source      string `json:"source"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Category    string `json:"category,omitempty"`
	Date        string `json:"date"`
	Size        string `json:"size,omitempty"`
	URL         string `json:"url"`
	Image       string `json:"image,omitempty"`
	AfterCutoff bool   `json:"after_cutoff,omitempty"`
}

type SearchResults struct {
	GameBanana []SearchResult `json:"gamebanana"`
	Skymods    []SearchResult `json:"skymods"`
	Errors     []string       `json:"errors,omitempty"`
}

func Search(q string, page int) SearchResults {
	var r SearchResults
	if gb, err := sources.GBSearch(q, page); err != nil {
		r.Errors = append(r.Errors, "GameBanana: "+err.Error())
	} else {
		for _, m := range gb {
			r.GameBanana = append(r.GameBanana, SearchResult{
				Ref: fmt.Sprintf("gb:%d", m.ID), Source: "GameBanana", Name: m.Name, Author: m.Submitter.Name,
				Category: m.Category.Name, Date: time.Unix(m.Modified, 0).Format("2006-01-02"), URL: m.URL, Image: m.Thumb(),
			})
		}
	}
	var sky []sources.SkyItem
	var err error
	if strings.TrimSpace(q) == "" {
		sky, err = sources.SkyLatest(page)
	} else {
		sky, err = sources.SkySearch(q, page)
	}
	if err != nil {
		r.Errors = append(r.Errors, "Skymods: "+err.Error())
	}
	for _, it := range sky {
		r.Skymods = append(r.Skymods, SearchResult{
			Ref: "sky:" + it.WorkshopID, Source: "Skymods", Name: it.Title, Author: it.Author,
			Date: FmtTime(it.Revision), Size: it.Size, URL: "https://steamcommunity.com/sharedfiles/filedetails/?id=" + it.WorkshopID, Image: it.Image,
			AfterCutoff: !it.Revision.IsZero() && !it.Revision.Before(manager.WormCutoff),
		})
	}
	return r
}

// ---- install ----

var reGBURL = regexp.MustCompile(`gamebanana\.com/mods/(\d+)`)
var reWSURL = regexp.MustCompile(`steamcommunity\.com/(?:sharedfiles|workshop)/filedetails/\?id=(\d+)`)

// Install installs gb:<id>, sky:<workshop id>, a GameBanana URL or a Steam
// Workshop URL.
func (a *App) Install(m *manager.Manager, ref string) error {
	ref = strings.TrimSpace(ref)
	if mm := reGBURL.FindStringSubmatch(ref); mm != nil {
		ref = "gb:" + mm[1]
	} else if mm := reWSURL.FindStringSubmatch(ref); mm != nil {
		ref = "sky:" + mm[1]
	}
	switch {
	case strings.HasPrefix(ref, "gb:"):
		id, err := strconv.Atoi(strings.TrimPrefix(ref, "gb:"))
		if err != nil {
			return fmt.Errorf("bad GameBanana id %q", ref)
		}
		return a.InstallGB(m, id, a.Opt.FileID, nil)
	case strings.HasPrefix(ref, "sky:"):
		return a.InstallSky(m, strings.TrimPrefix(ref, "sky:"))
	}
	return fmt.Errorf("unknown reference %q; use gb:<id>, sky:<workshop id>, or a GameBanana/Steam Workshop link", ref)
}

// InstallGB downloads and installs a GameBanana file. prev is non-nil for updates.
func (a *App) InstallGB(m *manager.Manager, id, fileID int, prev *manager.Installed) error {
	mod, err := sources.GBGetMod(id)
	if err != nil {
		return err
	}
	files, err := sources.GBFiles(id)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("gb:%d has no files", id)
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
			return fmt.Errorf("file %d not found in gb:%d", fileID, id)
		}
	}
	if prev != nil && prev.FileID == f.ID {
		return nil
	}
	if !f.Clean() {
		return &manager.Rejection{Reasons: []string{fmt.Sprintf("GameBanana malware analysis for %s is %q/%q/%q, not clean", f.Name, f.AVState, f.AVResult, f.Analysis)}}
	}
	a.logf("gb:%d %s - file %s (%s, uploaded %s)", id, mod.Name, f.Name, HumanSize(f.Size), f.AddedTime().Format("2006-01-02 15:04"))
	dl, err := downloadsDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dl, fmt.Sprintf("gb-%d-%d-%s", id, f.ID, filepath.Base(f.Name)))
	md5hex, shahex, err := sources.Download(f.DownloadURL, path, 1<<30)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if f.MD5 != "" && !strings.EqualFold(md5hex, f.MD5) {
		return &manager.Rejection{Reasons: []string{fmt.Sprintf("checksum mismatch: GameBanana says %s, got %s", f.MD5, md5hex)}}
	}
	return m.Install(&manager.Candidate{
		Key: fmt.Sprintf("gb:%d", id), Name: mod.Name, Source: mod.URL, Path: path,
		FileID: f.ID, Version: f.Version, Revision: f.AddedTime(), ArchiveSHA: shahex,
	})
}

func downloadsDir() (string, error) {
	d, err := manager.ConfigDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(d, "downloads")
	return p, os.MkdirAll(p, 0o755)
}

// InstallSky installs the Skymods mirror copy of a Workshop item. It goes
// through modsbase.com's own "create download link" step; if modsbase shows a
// Cloudflare check or captcha, it opens the page in the browser and imports
// the file once it appears in the Downloads folder.
func (a *App) InstallSky(m *manager.Manager, ws string) error {
	it, err := sources.SkyByWorkshopID(ws)
	if err != nil {
		return err
	}
	a.logf("Skymods: %s by %s (Workshop %s), revision %s, %s", it.Title, it.Author, it.WorkshopID, FmtTime(it.Revision), it.Size)
	if !it.Revision.IsZero() && !it.Revision.Before(manager.WormCutoff) && !a.Opt.Policy.AllowAfterCutoff {
		return &manager.Rejection{Reasons: []string{fmt.Sprintf("mirrored copy was revised %s, on/after the worm cutoff; it may contain the worm (override: --allow-after-cutoff)", it.Revision.Format("2006-01-02 15:04"))}}
	}
	if it.DownloadURL == "" {
		return fmt.Errorf("no download link on %s", it.PageURL)
	}
	b := a.with(func(o *Options) {
		o.WorkshopID = ws
		if o.Name == "" {
			o.Name = it.Title
		}
	})
	if file, err := a.skyDirect(it); err == nil {
		defer os.Remove(file)
		return b.Import(m, file)
	} else {
		a.logf("direct download not possible (%v); using the browser instead", err)
	}

	dl := a.Opt.Downloads
	if dl == "" {
		dl = DownloadFolder()
	}
	a.logf("Opening the download page in your browser: %s", it.DownloadURL)
	a.logf("Click the real download button (ignore ads; never run an .exe).")
	if a.Opt.NoWatch || dl == "" {
		a.logf("Then import the file with workshop id %s", ws)
		OpenBrowser(it.DownloadURL)
		return nil
	}
	since := time.Now()
	OpenBrowser(it.DownloadURL)
	a.logf("Waiting for %s_* in %s (up to %s)...", ws, dl, a.Opt.Wait)
	file, err := waitForDownload(dl, ws, since, a.Opt.Wait)
	if err != nil {
		return fmt.Errorf("%v; when you have the file, import it with workshop id %s", err, ws)
	}
	a.logf("got %s", filepath.Base(file))
	return b.Import(m, file)
}

func (a *App) skyDirect(it *sources.SkyItem) (string, error) {
	a.logf("requesting download link from modsbase.com...")
	link, err := sources.ModsbaseResolve(it.DownloadURL)
	if err != nil {
		return "", err
	}
	dir, err := downloadsDir()
	if err != nil {
		return "", err
	}
	name := filepath.Base(strings.TrimSuffix(it.DownloadURL, ".html"))
	path := filepath.Join(dir, "sky-"+it.WorkshopID+"-"+name)
	if _, err := sources.ModsbaseDownload(link, it.DownloadURL, path, 1<<30); err != nil {
		return "", err
	}
	a.logf("downloaded %s", name)
	return path, nil
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
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}
	ws := a.Opt.WorkshopID
	if ws == "" && st.IsDir() && reWorkshopDir.MatchString(filepath.Base(abs)) {
		ws = filepath.Base(abs) // a folder copied from steamapps/workshop/content/1118200
	}
	c := &manager.Candidate{Path: abs, Name: a.Opt.Name}
	if !st.IsDir() {
		if c.ArchiveSHA, err = manager.FileSHA(abs); err != nil {
			return err
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
				return err
			}
			c.ArchiveSHA = h
		}
		c.Key, c.Source = "local:"+c.ArchiveSHA[:12], abs
	}
	if c.Name == "" {
		c.Name = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	}
	return m.Install(c)
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
		a.tally(&s, inst.Key, "HELD", a.InstallGB(m, id, 0, inst))
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
	Libraries []string `json:"libraries"`
	Game      string   `json:"game"`
	Mods      string   `json:"mods"`
	GameError string   `json:"game_error,omitempty"`
	Workshop  []string `json:"workshop"`
	Data      string   `json:"data"`
	Downloads string   `json:"downloads"`
}

func (a *App) Paths() Paths {
	p := Paths{Libraries: game.Libraries(), Workshop: game.WorkshopDirs(), Downloads: DownloadFolder()}
	if dir, err := game.FindGameDir(a.Opt.Game); err == nil {
		p.Game, p.Mods = dir, game.ModsDir(dir)
	} else {
		p.GameError = err.Error()
	}
	p.Data, _ = manager.ConfigDir()
	return p
}
