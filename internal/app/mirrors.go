package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/scan"
	"github.com/DogeKingC/SWG/internal/sources"
)

// Mirror is one copy of a deleted Steam Workshop item on a mirror site.
// Mirrors can hold different revisions of the same item.
type Mirror struct {
	ID          string    `json:"id"` // skymods:<archive id> or topmods:<item id>
	Source      string    `json:"source"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	Version     string    `json:"version"`
	VersionTime time.Time `json:"version_time"`
	Size        string    `json:"size,omitempty"`
	Page        string    `json:"page"`
	Image       string    `json:"image,omitempty"`
	AfterCutoff bool      `json:"after_cutoff"`

	sky *sources.SkyItem
	tm  *sources.TMItem
}

var reSkyArchive = regexp.MustCompile(`/archives/(\d+)`)

func skyMirror(it sources.SkyItem) Mirror {
	return Mirror{
		ID: "skymods:" + first(reSkyArchive, it.PageURL), Source: "Skymods", Title: it.Title, Author: it.Author,
		Version: FmtTime(it.Revision), VersionTime: it.Revision, Size: it.Size, Page: it.PageURL, Image: it.Image,
		AfterCutoff: !it.Revision.IsZero() && !it.Revision.Before(manager.WormCutoff), sky: &it,
	}
}

func tmMirror(it *sources.TMItem) Mirror {
	return Mirror{
		ID: "topmods:" + it.ID, Source: "top-mods", Title: it.Title, Author: it.Author,
		Version: it.Version, VersionTime: it.VersionTime, Size: it.Size, Page: it.URL, Image: it.Image,
		AfterCutoff: !it.VersionTime.IsZero() && !it.VersionTime.Before(manager.WormCutoff), tm: it,
	}
}

func first(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// What ppgmods has learned about Workshop items from searches: titles (to
// look an item up on top-mods, whose search does not index Workshop IDs)
// and top-mods pages already known to hold it.
var (
	knownMu    sync.Mutex
	knownTitle = map[string]string{}
	knownTM    = map[string]map[string]bool{}
	mirrorMu   sync.Mutex
	mirrorMemo = map[string]mirrorMemoEntry{}
)

type mirrorMemoEntry struct {
	list []Mirror
	at   time.Time
}

func rememberTitle(ws, title, tmURL string) {
	knownMu.Lock()
	defer knownMu.Unlock()
	if title != "" && knownTitle[ws] == "" {
		knownTitle[ws] = title
	}
	if tmURL != "" {
		if knownTM[ws] == nil {
			knownTM[ws] = map[string]bool{}
		}
		knownTM[ws][tmURL] = true
	}
}

// WorkshopMirrors finds every mirror copy of a Workshop item, newest
// revision first. titleHint helps find it on top-mods.
func WorkshopMirrors(ws, titleHint string) ([]Mirror, error) {
	mirrorMu.Lock()
	if e, ok := mirrorMemo[ws]; ok && time.Since(e.at) < 15*time.Minute {
		mirrorMu.Unlock()
		return e.list, nil
	}
	mirrorMu.Unlock()

	var list []Mirror
	var errs []string
	if copies, err := sources.SkyCopies(ws); err == nil {
		for _, it := range copies {
			list = append(list, skyMirror(it))
			if titleHint == "" {
				titleHint = it.Title
			}
		}
	} else if !strings.Contains(err.Error(), "not found") {
		errs = append(errs, "Skymods: "+err.Error())
	}
	knownMu.Lock()
	if titleHint == "" {
		titleHint = knownTitle[ws]
	}
	tmURLs := map[string]bool{}
	for u := range knownTM[ws] {
		tmURLs[u] = true
	}
	knownMu.Unlock()
	// top-mods search matches whole words, so retry with a shorter title
	// ("Greenbrick Industries(R E U P L O A D)" -> "Greenbrick").
	for _, q := range titleQueries(titleHint) {
		res, err := sources.TMSearch(q, 1)
		if err != nil {
			errs = append(errs, "top-mods: "+err.Error())
			break
		}
		for i, r := range res {
			if i < 8 {
				tmURLs[r.URL] = true
			}
		}
		found := false
		for _, it := range tmDetailsAll(keys(tmURLs)) {
			if it.WorkshopID == ws {
				found = true
			}
		}
		if found {
			break
		}
	}
	for _, it := range tmDetailsAll(keys(tmURLs)) {
		if it.WorkshopID == ws {
			list = append(list, tmMirror(it))
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i].VersionTime, list[j].VersionTime
		if !a.Equal(b) {
			return a.After(b)
		}
		return list[i].Source == "Skymods" && list[j].Source != "Skymods" // exact time over day-only
	})
	if len(list) == 0 {
		if len(errs) > 0 {
			return nil, errors.New(strings.Join(errs, "; "))
		}
		return nil, fmt.Errorf("Workshop item %s was not found on Skymods or top-mods (search for it by name first)", ws)
	}
	mirrorMu.Lock()
	mirrorMemo[ws] = mirrorMemoEntry{list, time.Now()}
	mirrorMu.Unlock()
	return list, nil
}

var reWord = regexp.MustCompile(`[\p{L}\p{N}]+`)

func titleQueries(title string) []string {
	if strings.TrimSpace(title) == "" {
		return nil
	}
	words := reWord.FindAllString(title, -1)
	out := []string{title}
	if len(words) > 2 {
		out = append(out, strings.Join(words[:2], " "))
	}
	if len(words) > 1 && len(words[0]) >= 4 {
		out = append(out, words[0])
	}
	return out
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tmDetailsAll fetches top-mods item pages, a few at a time.
func tmDetailsAll(urls []string) []*sources.TMItem {
	out := make([]*sources.TMItem, len(urls))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if it, err := sources.TMDetails(u); err == nil {
				out[i] = it
			}
		}(i, u)
	}
	wg.Wait()
	var res []*sources.TMItem
	for _, it := range out {
		if it != nil {
			res = append(res, it)
		}
	}
	return res
}

// fetchWorkshop downloads a Workshop item from its mirrors. With
// Opt.Mirror set it uses that copy. Otherwise it takes the newest revision
// from before the worm cutoff, and if the scanner flags it (HIGH or worse)
// it falls back to the next-newest copy that scans clean.
func (a *App) fetchWorkshop(m *manager.Manager, ws string) (*manager.Candidate, error) {
	mirrors, err := WorkshopMirrors(ws, a.Opt.Name)
	if err != nil {
		return nil, err
	}
	if a.Opt.Mirror != "" {
		var pick []Mirror
		for _, mr := range mirrors {
			if mr.ID == a.Opt.Mirror {
				pick = append(pick, mr)
			}
		}
		if len(pick) == 0 {
			return nil, fmt.Errorf("mirror %s does not have Workshop item %s", a.Opt.Mirror, ws)
		}
		mirrors = pick
	}
	var usable []Mirror
	for _, mr := range mirrors {
		if mr.AfterCutoff && !a.Opt.Policy.AllowAfterCutoff {
			a.logf("  %s copy (%s) skipped: revised on/after the worm cutoff", mr.Source, mr.Version)
			continue
		}
		usable = append(usable, mr)
	}
	if len(usable) == 0 {
		return nil, &manager.Rejection{Reasons: []string{fmt.Sprintf("every mirror copy was revised on/after the worm cutoff %s; it may contain the worm (override: --allow-after-cutoff)", manager.WormCutoff.Format("2006-01-02"))}}
	}

	var browser *NeedsBrowser
	var gone []string
	var flagged *manager.Candidate
	for i, mr := range usable {
		a.logf("%s: %s by %s (Workshop %s), version %s, %s", mr.Source, mr.Title, mr.Author, ws, mr.Version, mr.Size)
		path, err := a.downloadMirror(mr, ws)
		var nb *NeedsBrowser
		switch {
		case errors.Is(err, sources.ErrGone):
			gone = append(gone, mr.Source+": "+err.Error())
			a.logf("  %v", err)
			continue
		case errors.As(err, &nb):
			if browser == nil {
				browser = nb
			}
			continue
		case err != nil:
			a.logf("  %s: %v", mr.Source, err)
			gone = append(gone, mr.Source+": "+err.Error())
			continue
		}
		b := a.with(func(o *Options) {
			o.WorkshopID, o.Revision = ws, mr.VersionTime
			if o.Name == "" {
				o.Name = mr.Title
			}
		})
		c, err := b.candidate(path)
		if err != nil {
			return nil, err
		}
		c.Mirror, c.Version, c.Source = mr.ID, mr.Version, mr.Page
		if a.Opt.Mirror != "" {
			return c, nil // the user picked this copy; the install policy still applies
		}
		ok, max := scansClean(m, c)
		if ok {
			if flagged != nil {
				a.logf("  using this older copy because the newer one was flagged by the scanner")
			}
			return c, nil
		}
		if i < len(usable)-1 {
			a.logf("  scanner flagged this copy (%s); trying an older copy", max)
		}
		if flagged == nil {
			flagged = c
		}
	}
	if flagged != nil {
		return flagged, nil
	}
	if browser != nil {
		return nil, browser
	}
	return nil, &Unavailable{Reason: "This mod can't be downloaded from any mirror (" + strings.Join(gone, "; ") + ")."}
}

// scansClean reports whether a candidate has no HIGH or CRITICAL findings.
func scansClean(m *manager.Manager, c *manager.Candidate) (bool, string) {
	if m == nil {
		return true, ""
	}
	dir, rep, err := m.Stage(c)
	if err != nil {
		return false, err.Error()
	}
	os.RemoveAll(dir)
	if rep.Max() >= scan.High {
		return false, rep.Max().String()
	}
	return true, ""
}

// downloadMirror downloads one mirror copy into the cache (reusing an
// earlier download) and returns its path.
func (a *App) downloadMirror(mr Mirror, ws string) (string, error) {
	dir, err := CacheDir("sky:" + ws)
	if err != nil {
		return "", err
	}
	prefix := strings.ReplaceAll(mr.ID, ":", "-") + "-"
	if ents, _ := os.ReadDir(dir); ents != nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), prefix) && !strings.HasSuffix(e.Name(), ".part") {
				if info, err := e.Info(); err == nil && info.Size() > 0 {
					return filepath.Join(dir, e.Name()), nil
				}
			}
		}
	}
	var links []string
	switch {
	case mr.sky != nil:
		if mr.sky.DownloadURL == "" {
			return "", fmt.Errorf("%w: Skymods has no download for this copy", sources.ErrGone)
		}
		links = []string{mr.sky.DownloadURL}
	case mr.tm != nil:
		links = mr.tm.Downloads
	}
	if len(links) == 0 {
		return "", fmt.Errorf("%w: no download link", sources.ErrGone)
	}
	var lastErr error
	var browser *NeedsBrowser
	for _, link := range links {
		name := filepath.Base(strings.TrimSuffix(link, ".html"))
		if !strings.Contains(name, ".") {
			name = ws + ".zip"
		}
		path := filepath.Join(dir, prefix+name)
		part := path + ".part"
		switch {
		case strings.HasPrefix(link, "https://modsfire.com/"):
			a.logf("  downloading from modsfire.com...")
			_, err = sources.ModsfireDownload(link, mr.Page, part, 1<<30)
		case strings.HasPrefix(link, "https://modsbase.com/"):
			a.logf("  requesting download link from modsbase.com...")
			var direct string
			if direct, err = sources.ModsbaseResolve(link); err == nil {
				_, err = sources.ModsbaseDownload(direct, link, part, 1<<30)
			}
		default:
			err = fmt.Errorf("unsupported download host: %s", link)
		}
		if err == nil {
			if err := os.Rename(part, path); err != nil {
				return "", err
			}
			a.logf("  downloaded %s", name)
			return path, nil
		}
		os.Remove(part)
		a.logf("  %s: %v", hostOf(link), err)
		lastErr = err
		if !errors.Is(err, sources.ErrGone) && browser == nil {
			browser = &NeedsBrowser{URL: link, Reason: err.Error(), WorkshopID: ws}
		}
	}
	if browser != nil {
		return "", browser
	}
	return "", lastErr
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		return u[:i]
	}
	return u
}
