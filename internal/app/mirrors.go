package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Trlydev/SWG/internal/loaders"
	"github.com/Trlydev/SWG/internal/manager"
	"github.com/Trlydev/SWG/internal/popularity"
	"github.com/Trlydev/SWG/internal/preserve"
	"github.com/Trlydev/SWG/internal/scan"
	"github.com/Trlydev/SWG/internal/sources"
	"github.com/Trlydev/SWG/internal/version"
	"github.com/Trlydev/SWG/internal/workshop"
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
	ModVersion  string    `json:"mod_version,omitempty"`   // from the copy's mod.json, once downloaded
	TitleVer    string    `json:"title_version,omitempty"` // version written in the listing's title, e.g. "V:2.8"
	Gone        bool      `json:"gone,omitempty"`          // the mirror no longer has the file
	invalid     string    // set once downloaded: why the copy is not offered
	Reviewed    bool      `json:"reviewed,omitempty"` // True Workshop: reviewed by its maintainers
	Browser     bool      `json:"browser,omitempty"`  // downloaded in the person's browser (01 STUDIO: needs their account)
	Archived    string    `json:"archived,omitempty"` // pre-worm archive: "recorded" (hash known), "verified" (downloaded and matches)

	sky *sources.SkyItem
	tm  *sources.TMItem
	tw  *sources.TWItem
	s01 *sources.S01Mod
	nx  *sources.NXMod
	ow  *workshop.Entry
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
		Version: YMD(it.Version), VersionTime: it.VersionTime, Size: it.Size, Page: it.URL, Image: it.Image,
		AfterCutoff: !it.VersionTime.IsZero() && !it.VersionTime.Before(manager.WormCutoff), tm: it,
	}
}

func mirrorDownloads(mr Mirror) int {
	n := 0
	if mr.tm != nil && mr.tm.Views > n {
		n = mr.tm.Views
	}
	if mr.s01 != nil && mr.s01.Views > n {
		n = mr.s01.Views
	}
	if mr.nx != nil && mr.nx.Downloads > n {
		n = mr.nx.Downloads
	}
	if mr.ow != nil && mr.ow.Downloads > n {
		n = mr.ow.Downloads
	}
	if mr.tw != nil && mr.tw.Downloads > n {
		n = mr.tw.Downloads
	}
	return n
}

// twMirror is a True Workshop upload of a Workshop item. Its date is the
// upload date, not the Steam revision, so copies are compared by the
// ModVersion in mod.json instead.
// s01Mirror is the author's own copy on 01studio.dev. Its files are only
// given to signed-in users, so it is downloaded in the browser, when picked.
func s01Mirror(it sources.S01Mod) Mirror {
	v := "on 01studio.dev"
	if it.Version != "" {
		v = "v" + it.Version + " on 01studio.dev"
	}
	return Mirror{
		ID: "01studio:" + it.Slug, Source: "01 STUDIO", Title: it.Title, Author: "01 STUDIO",
		Version: v, VersionTime: it.CreatedTime(), Page: it.Page(), Image: it.Image(), Browser: true, s01: &it,
	}
}

// nxMirror is an upload on Nexus Mods, downloaded in the browser when picked.
func nxMirror(it sources.NXMod) Mirror {
	v := "on Nexus Mods"
	if it.Version != "" {
		v = "v" + it.Version + " on Nexus Mods"
	}
	return Mirror{
		ID: fmt.Sprintf("nexus:%d", it.ID), Source: "Nexus Mods", Title: it.Name, Author: it.Author,
		Version: v, VersionTime: it.UpdatedTime(), Page: it.FilesPage(), Image: it.Thumbnail, Browser: true, nx: &it,
	}
}

func twMirror(it sources.TWItem) Mirror {
	return Mirror{
		ID: fmt.Sprintf("trueworkshop:%d", it.ID), Source: "True Workshop", Title: it.Title, Author: it.Author,
		Version: "uploaded " + FmtTime(it.CreatedTime()), Size: HumanSize(it.Size), Page: it.Page(), Image: it.Thumb(),
		Reviewed: it.Reviewed(), tw: &it,
	}
}

var reTrailingVersion = regexp.MustCompile(`(?i)^(.*?)[\s:_-]*(?:v(?:er(?:sion)?)?)?[\s:._-]*\d+(?:\.\d+)*[a-z]?$`)

// normTitle reduces a mod name to lowercase letters and digits.
func normTitle(s string) string { return strings.Join(sources.SlugWords(s), "") }

// sameMod reports whether two listings name the same mod: equal names (or
// equal apart from a trailing version number) and, when both have one, the
// same author.
func sameMod(titleA, authorA, titleB, authorB string) bool {
	if authorA != "" && authorB != "" && normTitle(authorA) != normTitle(authorB) {
		return false
	}
	a, b := normTitle(titleA), normTitle(titleB)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	strip := func(t string) string {
		if m := reTrailingVersion.FindStringSubmatch(t); m != nil && m[1] != "" {
			return normTitle(m[1])
		}
		return normTitle(t)
	}
	if strip(titleA) == strip(titleB) {
		return true
	}
	// Tags in brackets: "Jujutsu Playground [RELEASE]", "Ship (Reupload)".
	ta, tb := reTitleTags.ReplaceAllString(titleA, " "), reTitleTags.ReplaceAllString(titleB, " ")
	return (ta != titleA || tb != titleB) && normTitle(ta) != "" && strip(ta) == strip(tb)
}

var reTitleTags = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)

// CompareVersions compares mod.json ModVersion strings numerically
// ("4.0" > "3.2", "1.75.2" > "1.70.8"). Unknown versions sort lowest.
func CompareVersions(a, b string) int { return version.Compare(a, b) }

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
	useIndexCache()
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
	// The Open Workshop: the author's own, reviewed republication.
	if e := owForWorkshop(ws); e != nil {
		list = append(list, owMirror(*e))
		if titleHint == "" {
			titleHint = e.Name
		}
	}
	// 01 STUDIO's own site lists its mods with their Workshop IDs.
	// Only a free file: Early Access versions are for paying subscribers.
	if it, err := sources.S01ByWorkshopID(ws); err == nil && it != nil && it.SiteFileFree() {
		list = append(list, s01Mirror(*it))
		if titleHint == "" {
			titleHint = it.Title
		}
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
	// top-mods: match the name against the site's sitemap (every PPG item);
	// fall back to the site's own search if the sitemap is unavailable.
	if titleHint != "" {
		res, err := sources.TMFind(titleHint, 8)
		if err != nil || len(res) == 0 {
			for _, q := range titleQueries(titleHint) {
				if r2, err2 := sources.TMSearch(q, 1); err2 == nil && len(r2) > 0 {
					res = r2
					break
				} else if err2 != nil {
					errs = append(errs, "top-mods: "+err2.Error())
					break
				}
			}
		}
		for i, r := range res {
			if i < 8 {
				tmURLs[r.URL] = true
			}
		}
	}
	for _, it := range tmDetailsAll(keys(tmURLs)) {
		if it.WorkshopID == ws {
			list = append(list, tmMirror(it))
		}
	}
	// True Workshop uploads carry no Workshop ID in the listing: match by
	// name and author here, and confirm with mod.json's CreatorUGCIdentity
	// once downloaded.
	if titleHint != "" {
		author := ""
		for _, mr := range list {
			if mr.Author != "" {
				author = mr.Author
				break
			}
		}
		if all, err := sources.TWAll(); err == nil {
			for _, it := range all {
				if it.Type == "mod" && sameMod(it.Title, it.Author, titleHint, author) && !ugcMismatch(fmt.Sprintf("trueworkshop:%d", it.ID), ws) {
					list = append(list, twMirror(it))
				}
			}
		}
		// Nexus Mods: same name, uploaded by the same person or studio.
		authors := []string{author}
		for _, mr := range list {
			if mr.s01 != nil {
				authors = append(authors, "01 STUDIO")
			}
		}
		if all, err := sources.NXAll(); err == nil {
			infos := sources.NXInfos(all)
			for _, it := range all {
				if infos[it.ID].WorkshopID == ws {
					list = append(list, nxMirror(it)) // its folder carries this Workshop ID
					continue
				}
				if w := infos[it.ID].WorkshopID; w != "" || infos[it.ID].Kind == sources.KindContraption {
					continue // another Workshop item, or not a mod
				}
				for _, au := range authors {
					if au != "" && (sameMod(it.Name, it.Author, titleHint, au) || sameMod(it.Name, it.Uploader, titleHint, au)) {
						list = append(list, nxMirror(it))
						break
					}
				}
			}
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i].VersionTime, list[j].VersionTime
		if !a.Equal(b) {
			return a.After(b) // unknown revision (True Workshop) last
		}
		return list[i].Source == "Skymods" && list[j].Source != "Skymods" // exact time over day-only
	})
	fillModVersions(list)
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

// What downloaded copies' mod.json said, per mirror copy id.
var (
	modInfoMu sync.Mutex
	modInfo   = map[string]copyInfo{}
)

type copyInfo struct {
	version, ugc string
	gone         bool
	invalid      string // why the copy is not offered: no mod.json, no author
	kind         string // mod or contraption, from the downloaded files
}

// What was learned about mirror copies is kept on disk for a week, so a
// copy found gone or invalid once is not shown as an option again, and a
// card shows the version the copy's mod.json had.
type savedCopy struct {
	Version string    `json:"version,omitempty"`
	UGC     string    `json:"ugc,omitempty"`
	Gone    bool      `json:"gone,omitempty"`
	Invalid string    `json:"invalid,omitempty"`
	Kind    string    `json:"kind,omitempty"`
	At      time.Time `json:"at"`
}

var modInfoLoaded bool

func modInfoPath() string {
	useIndexCache()
	if sources.IndexCacheDir == "" {
		return ""
	}
	return filepath.Join(sources.IndexCacheDir, "mirror-copies.json")
}

// loadModInfo reads the saved copy info; call with modInfoMu held.
func loadModInfo() {
	if modInfoLoaded {
		return
	}
	modInfoLoaded = true
	p := modInfoPath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var saved map[string]savedCopy
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	for id, c := range saved {
		if time.Since(c.At) < 7*24*time.Hour {
			if _, ok := modInfo[id]; !ok {
				modInfo[id] = copyInfo{version: c.Version, ugc: c.UGC, gone: c.Gone, invalid: c.Invalid, kind: c.Kind}
			}
		}
	}
}

// setCopyInfo records what a download showed and saves it.
func setCopyInfo(id string, ci copyInfo) {
	modInfoMu.Lock()
	defer modInfoMu.Unlock()
	loadModInfo()
	modInfo[id] = ci
	p := modInfoPath()
	if p == "" {
		return
	}
	saved := map[string]savedCopy{}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &saved)
	}
	saved[id] = savedCopy{Version: ci.version, UGC: ci.ugc, Gone: ci.gone, Invalid: ci.invalid, Kind: ci.kind, At: time.Now()}
	if b, err := json.Marshal(saved); err == nil {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, b, 0o644)
	}
}

// copyState returns what is known about a mirror copy.
func copyState(id string) copyInfo {
	modInfoMu.Lock()
	defer modInfoMu.Unlock()
	loadModInfo()
	return modInfo[id]
}

// bestVersion is a copy's mod.json version if known, else its title's.
func bestVersion(id, title string) string {
	if v := copyState(id).version; v != "" {
		return v
	}
	return TitleVersion(title)
}

// Authors learned from downloaded copies' mod.json, by Workshop ID, for
// listings that do not name one.
var (
	authorMu      sync.Mutex
	learnedAuthor = map[string]string{}
)

func learnAuthor(ws, author string) {
	if ws == "" || strings.TrimSpace(author) == "" {
		return
	}
	authorMu.Lock()
	learnedAuthor[ws] = strings.TrimSpace(author)
	authorMu.Unlock()
}

func knownAuthor(ws string) string {
	authorMu.Lock()
	defer authorMu.Unlock()
	return learnedAuthor[ws]
}

func fillModVersions(list []Mirror) {
	modInfoMu.Lock()
	defer modInfoMu.Unlock()
	loadModInfo()
	for i := range list {
		ci := modInfo[list[i].ID]
		list[i].ModVersion, list[i].Gone, list[i].invalid = ci.version, ci.gone, ci.invalid
		if r := archiveRecordNoLock(list[i]); r != nil && r.SHA256 != "" {
			list[i].Archived = "recorded"
			if ci.version != "" || ci.kind != "" {
				list[i].Archived = "verified" // downloaded, and it passed the comparison
			}
			if r.Stored != "" && ci.gone {
				list[i].Gone = false // the archive still has it
			}
		}
		list[i].TitleVer = TitleVersion(list[i].Title)
	}
}

// offered drops the copies a person should not be offered: files the mirror
// no longer has, and downloaded copies that turned out to have no mod.json
// (not a People Playground mod) or no author anywhere.
func offered(list []Mirror) []Mirror {
	var out []Mirror
	for _, mr := range list {
		if !mr.Gone && mr.invalid == "" {
			out = append(out, mr)
		}
	}
	return out
}

var reTitleVersion = regexp.MustCompile(`(?i)(?:\bv(?:er(?:sion)?)?\s*[:.]?\s*|\s)(\d+(?:\.\d+)*[a-z]?)\s*\)?\s*$`)

// TitleVersion returns a version number written at the end of a title
// ("Science Hazard Minus V:2.8" -> "2.8", "Mod 1.2.3" -> "1.2.3").
func TitleVersion(title string) string {
	if m := reTitleVersion.FindStringSubmatch(strings.TrimSpace(title)); m != nil && strings.ContainsAny(m[0], "vV.") {
		return m[1]
	}
	return ""
}

// ugcMismatch reports whether a downloaded copy turned out to belong to a
// different Workshop item.
func ugcMismatch(id, ws string) bool {
	modInfoMu.Lock()
	defer modInfoMu.Unlock()
	loadModInfo()
	u := modInfo[id].ugc
	return u != "" && u != ws
}

// useIndexCache points the sources' on-disk index cache at ppgmods' data.
func useIndexCache() {
	if sources.IndexCacheDir == "" {
		if d, err := manager.ConfigDir(); err == nil {
			sources.IndexCacheDir = filepath.Join(d, "cache")
		}
	}
	if preserve.CacheDir == "" {
		preserve.CacheDir = sources.IndexCacheDir
	}
	if loaders.CacheDir == "" {
		loaders.CacheDir = sources.IndexCacheDir
	}
	if workshop.CacheDir == "" {
		workshop.CacheDir = sources.IndexCacheDir
	}
	if popularity.CacheDir == "" {
		popularity.CacheDir = sources.IndexCacheDir
	}
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

	// Download the copies (newest revision first; at most four), read each
	// one's mod.json, then rank by ModVersion: mirror dates can mislead
	// (True Workshop dates are upload dates).
	type copyC struct {
		mr      Mirror
		c       *manager.Candidate
		version string
		clean   bool
		max     string
	}
	var copies []copyC
	var browser *NeedsBrowser
	var gone []string
	for _, mr := range usable {
		if len(copies) == 4 {
			break
		}
		nexusDirect := mr.nx != nil && nexusPremium()
		if (mr.s01 != nil || mr.nx != nil) && !nexusDirect {
			nb := &NeedsBrowser{URL: mr.Page, WorkshopID: ws, AnyFile: true, Mirror: mr.ID,
				Reason: "01 STUDIO gives its files to signed-in users (a free account)"}
			if mr.nx != nil {
				nb = nexusBrowser(*mr.nx, "")
				nb.WorkshopID = ws
			}
			if a.Opt.Mirror == mr.ID {
				return nil, nb // picked: download it in the browser
			}
			browser = easierBrowser(browser, nb) // only if no mirror can provide a copy
			continue
		}
		rec := archiveRecord(mr)
		archived := rec != nil && rec.Stored != "" && rec.SHA256 != ""
		if ci := copyState(mr.ID); a.Opt.Mirror == "" && (ci.gone && !archived || ci.invalid != "") {
			why := ci.invalid
			if ci.gone {
				why = "the file is gone"
			}
			a.logf("  %s copy skipped: %s (checked earlier)", mr.Source, why)
			gone = append(gone, mr.Source+": "+why)
			continue
		}
		a.logf("%s: %s%s (Workshop %s), %s, %s", mr.Source, mr.Title, By(mr.Author), ws, mr.Version, mr.Size)
		var path string
		var err error
		if nexusDirect {
			path, _, err = a.downloadNexus(mr.nx.ID, nil) // linked premium account
		} else if mr.ow != nil {
			path, err = downloadOW(mr.ow)
		} else {
			path, err = a.downloadMirror(mr, ws)
			if errors.Is(err, sources.ErrGone) && archived {
				a.logf("  %v; using the pre-worm archive's copy", err)
				path, err = a.fromArchive(rec, ws)
			}
		}
		if err == nil && rec != nil && rec.SHA256 != "" {
			// The pre-worm archive recorded this copy: it must be the same file.
			if sum, serr := manager.FileSHA(path); serr == nil && sum != rec.SHA256 {
				why := fmt.Sprintf("the file is not the one the pre-worm archive recorded on %s (it was changed since)", FmtTime(rec.Archived))
				a.logf("  %s copy REFUSED: %s", mr.Source, why)
				setCopyInfo(mr.ID, copyInfo{invalid: why})
				os.Remove(path)
				if a.Opt.Mirror == mr.ID {
					return nil, &manager.Rejection{Reasons: []string{mr.Source + " copy: " + why + " (cannot be overridden)"}}
				}
				gone = append(gone, mr.Source+": "+why)
				continue
			} else if serr == nil {
				a.logf("  matches the pre-worm archive (recorded %s, scan %s)", FmtTime(rec.Archived), orUnknown(rec.ScanMax))
			}
		}
		var nb *NeedsBrowser
		switch {
		case errors.Is(err, sources.ErrGone):
			gone = append(gone, mr.Source+": "+err.Error())
			a.logf("  %v", err)
			setCopyInfo(mr.ID, copyInfo{gone: true})
			continue
		case errors.As(err, &nb):
			browser = easierBrowser(browser, nb)
			continue
		case err != nil:
			a.logf("  %s: %v", mr.Source, err)
			gone = append(gone, mr.Source+": "+err.Error())
			continue
		}
		b := a.with(func(o *Options) {
			o.WorkshopID, o.Revision = ws, mr.VersionTime
			if mr.tw != nil {
				o.Revision = mr.tw.CreatedTime()
			}
			if o.Name == "" {
				o.Name = mr.Title
			}
		})
		c, err := b.candidate(path)
		if err != nil {
			return nil, err
		}
		c.Mirror, c.Version, c.Source = mr.ID, mr.Version, mr.Page
		if mr.tw != nil {
			// A reviewed upload, not a Steam download: the worm-cutoff date
			// check does not apply; the cooldown applies unless reviewed.
			c.SteamOrig, c.Reviewed, c.Revision = false, mr.Reviewed, mr.tw.CreatedTime()
		}
		if mr.nx != nil {
			// From Nexus Mods, not Steam: no worm-cutoff check; the cooldown
			// applies (Nexus uploads aren't reviewed).
			c.SteamOrig, c.Revision = false, mr.VersionTime
		}
		if mr.ow != nil {
			// Checked automatically: the cooldown applies unless the owner
			// reviewed this version.
			c.SteamOrig, c.Reviewed, c.Revision = false, mr.ow.Reviewed, mr.ow.Published
		}
		in := inspect(m, c)
		version, ugc, clean, max := in.version, in.ugc, in.clean, in.max
		invalid := ""
		switch {
		case m == nil:
		case !in.modJSON && !in.contraption:
			invalid = "no mod.json, so it is not a People Playground mod"
		case !in.contraption && in.author == "" && strings.TrimSpace(mr.Author) == "":
			invalid = "no author in its mod.json or on the mirror page"
		}
		kind := ""
		switch {
		case in.contraption:
			kind = manager.KindContraption
		case in.modJSON:
			kind = manager.KindMod
		}
		setCopyInfo(mr.ID, copyInfo{version: version, ugc: ugc, invalid: invalid, kind: kind})
		if invalid != "" {
			a.logf("  this copy has %s; skipped", invalid)
			gone = append(gone, mr.Source+": "+invalid)
			continue
		}
		if ugc != "" && ugc != ws {
			a.logf("  this copy's mod.json belongs to Workshop item %s, not %s; skipped", ugc, ws)
			continue
		}
		if version != "" {
			c.Version = version
			if in.author != "" && strings.TrimSpace(mr.Author) == "" {
				a.logf("  mod.json version %s, author %s", version, in.author)
			} else {
				a.logf("  mod.json version %s", version)
			}
		}
		if a.Opt.Mirror != "" {
			return c, nil // the user picked this copy; the install policy still applies
		}
		copies = append(copies, copyC{mr, c, version, clean, max})
	}
	if len(copies) == 0 {
		if browser != nil {
			return nil, browser
		}
		return nil, &Unavailable{Reason: "This mod can't be downloaded from any mirror (" + strings.Join(gone, "; ") + ")."}
	}
	// Rank by mod.json version; when that is missing or equal (some authors
	// never bump it), by the version in the title, then by date.
	effective := func(cc copyC) string {
		if cc.version != "" {
			return cc.version
		}
		return TitleVersion(cc.mr.Title)
	}
	sort.SliceStable(copies, func(i, j int) bool {
		if c := CompareVersions(effective(copies[i]), effective(copies[j])); c != 0 {
			return c > 0
		}
		if c := CompareVersions(TitleVersion(copies[i].mr.Title), TitleVersion(copies[j].mr.Title)); c != 0 {
			return c > 0
		}
		if !copies[i].mr.VersionTime.Equal(copies[j].mr.VersionTime) {
			return copies[i].mr.VersionTime.After(copies[j].mr.VersionTime)
		}
		return copies[i].mr.Reviewed && !copies[j].mr.Reviewed
	})
	for i, cc := range copies {
		if cc.clean {
			if i > 0 {
				a.logf("using the %s copy (version %s): newer copies were flagged by the scanner", cc.mr.Source, orUnknown(cc.version))
			} else if len(copies) > 1 {
				a.logf("using the %s copy: highest version (%s)", cc.mr.Source, orUnknown(cc.version))
			}
			return cc.c, nil
		}
		a.logf("  %s copy (version %s) flagged by the scanner (%s)", cc.mr.Source, orUnknown(cc.version), cc.max)
	}
	return copies[0].c, nil // every copy is flagged: let the install policy report the newest
}

// easierBrowser picks the browser download that is easiest for the person:
// a mirror's page (no account), then Nexus Mods (a free account), then
// 01 STUDIO (a free account).
func easierBrowser(have, nb *NeedsBrowser) *NeedsBrowser {
	rank := func(b *NeedsBrowser) int {
		switch {
		case b == nil:
			return 9
		case strings.HasPrefix(b.Mirror, "01studio:"):
			return 2
		case strings.HasPrefix(b.Mirror, "nexus:"):
			return 1
		}
		return 0
	}
	if rank(nb) < rank(have) {
		return nb
	}
	return have
}

func nexusPremium() bool {
	a := LoadNexus()
	return a != nil && a.Premium
}

// archiveRecord is the pre-worm archive's record of a Workshop mirror copy.
func archiveRecord(mr Mirror) *preserve.Record {
	if mr.sky == nil && mr.tm == nil {
		return nil
	}
	return preserve.Lookup(mr.ID)
}

// fromArchive downloads the pre-worm archive's stored copy and checks it.
func (a *App) fromArchive(rec *preserve.Record, ws string) (string, error) {
	dir, err := CacheDir("sky:" + ws)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "archive-"+rec.SHA256[:16]+filepath.Ext(rec.Stored))
	if sum, err := manager.FileSHA(dest); err == nil && sum == rec.SHA256 {
		return dest, nil
	}
	_, sum, err := sources.Download(rec.Stored, dest+".part", 1<<30)
	if err != nil {
		os.Remove(dest + ".part")
		return "", err
	}
	if sum != rec.SHA256 {
		os.Remove(dest + ".part")
		return "", fmt.Errorf("the archive's copy doesn't match its record")
	}
	return dest, os.Rename(dest+".part", dest)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// inspected is what staging one mirror copy showed.
type inspected struct {
	version, ugc, author string
	modJSON, contraption bool
	clean                bool
	max                  string
}

// inspect stages a copy and reads its mod.json (ModVersion, Author and
// CreatorUGCIdentity, the Workshop ID) and whether it scans clean.
func inspect(m *manager.Manager, c *manager.Candidate) inspected {
	var in inspected
	if m == nil {
		in.clean = true
		return in
	}
	dir, rep, err := m.Stage(c)
	if err != nil {
		in.max = err.Error()
		return in
	}
	defer os.RemoveAll(dir)
	if roots, _ := modRoots(dir); len(roots) > 0 {
		var mj struct {
			ModVersion         string          `json:"ModVersion"`
			Author             string          `json:"Author"`
			CreatorUGCIdentity json.RawMessage `json:"CreatorUGCIdentity"`
		}
		if b, err := os.ReadFile(filepath.Join(roots[0], "mod.json")); err == nil {
			in.modJSON = json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &mj) == nil
		}
		in.version, in.author = strings.TrimSpace(mj.ModVersion), strings.TrimSpace(mj.Author)
		in.ugc = manager.UGCString(mj.CreatorUGCIdentity)
		learnAuthor(in.ugc, mj.Author)
	} else if names, _ := contraptions(dir); len(names) > 0 {
		in.contraption = true
	}
	if rep.Max() >= scan.High {
		in.max = rep.Max().String()
		return in
	}
	in.clean = true
	return in
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
	case mr.tw != nil:
		name := strings.ReplaceAll(filepath.Base(mr.tw.FileURL), " ", "_")
		path := filepath.Join(dir, prefix+name)
		a.logf("  downloading from True Workshop (Hugging Face)...")
		_, sha, err := sources.Download(mr.tw.DownloadURL(), path+".part", 1<<30)
		if err != nil {
			os.Remove(path + ".part")
			return "", err
		}
		if !strings.EqualFold(sha, mr.tw.SHA256) {
			os.Remove(path + ".part")
			return "", fmt.Errorf("checksum mismatch: True Workshop says %s, got %s", mr.tw.SHA256, sha)
		}
		go sources.TWTrackDownload(mr.tw.ID)
		return path, os.Rename(path+".part", path)
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
		supported := strings.HasPrefix(link, "https://modsfire.com/") || strings.HasPrefix(link, "https://modsbase.com/")
		if supported && !errors.Is(err, sources.ErrGone) && browser == nil {
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

// SkyMirror and TMMirror describe a mirror listing as a Mirror (used by the
// pre-worm archive job).
func SkyMirror(it sources.SkyItem) Mirror { return skyMirror(it) }
func TMMirror(it *sources.TMItem) Mirror  { return tmMirror(it) }

// DownloadCopy downloads one mirror copy of a Workshop item into the cache
// (used by the pre-worm archive job).
func (a *App) DownloadCopy(mr Mirror, ws string) (string, error) { return a.downloadMirror(mr, ws) }

// archiveRecordNoLock is archiveRecord without fetching (fillModVersions
// holds modInfoMu): only an archive already loaded is consulted.
func archiveRecordNoLock(mr Mirror) *preserve.Record {
	if mr.sky == nil && mr.tm == nil {
		return nil
	}
	return preserve.Cached(mr.ID)
}
