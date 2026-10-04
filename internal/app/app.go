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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DogeKingC/SWG/internal/game"
	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/popularity"
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

// By returns " by <author>", or "" when the author is unknown.
func By(author string) string {
	if a := strings.TrimSpace(author); a != "" {
		return " by " + a
	}
	return ""
}

// YMD rewrites a date as the sites print it (top-mods' 19.09.2026, True
// Workshop's 2026-09-27 15:31:50, RFC 3339) as 2026-09-19, the one format
// ppgmods shows. Anything else is returned unchanged.
func YMD(s string) string {
	s = strings.TrimSpace(s)
	if t := sources.ParseTMVersion(s); !t.IsZero() {
		return FmtTime(t)
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return FmtTime(t)
		}
	}
	return s
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
	Mirrors     []string `json:"mirrors,omitempty"` // Workshop: "top-mods 2026-09-19", ...
	Trend       string   `json:"trend,omitempty"`   // popularity sort: "+1,204 views this week"
	Version     string   `json:"version,omitempty"` // Workshop cards: highest known version of the copies

	newest  time.Time
	bestVer string // Workshop cards: highest version among the copies
	hasVer  bool
}

type SearchResults struct {
	GameBanana []SearchResult `json:"gamebanana"`
	TrueWS     []SearchResult `json:"trueworkshop"` // True Workshop uploads (maintainer-reviewed archive)
	Workshop   []SearchResult `json:"workshop"`     // deleted Steam Workshop items, merged across mirrors
	Studio01   []SearchResult `json:"studio01"`     // 01 STUDIO's own catalogue (each is also a Workshop item)
	Nexus      []SearchResult `json:"nexus"`        // Nexus Mods
	Errors     []string       `json:"errors,omitempty"`
	MergedTW   []string       `json:"merged_tw,omitempty"` // True Workshop refs shown inside a Workshop card
	Notes      []string       `json:"notes,omitempty"`     // how the results were chosen, when not obvious
}

// SearchOpts chooses what a search lists and in which order.
type SearchOpts struct {
	Kind   string // "mod" (default) or "contraption"
	Sort   string // "relevance" (default), "updated" or "popular"
	Period string // with "popular": "day", "week", "month", or "" for all time
}

func (o SearchOpts) normalized(q string) SearchOpts {
	if o.Kind != manager.KindContraption {
		o.Kind = manager.KindMod
	}
	switch o.Sort {
	case "updated", "popular":
	default:
		o.Sort = "relevance"
		if strings.TrimSpace(q) == "" {
			o.Sort = "updated" // nothing to be relevant to
		}
	}
	if _, ok := popularity.Periods[o.Period]; !ok || o.Sort != "popular" {
		o.Period = ""
	}
	return o
}

// Search queries GameBanana and both Workshop mirrors. Skymods and top-mods
// results for the same Workshop item are merged into one entry.
func Search(q string, page int) SearchResults {
	return SearchParts(q, page, map[string]bool{"gb": true, "tw": true, "ws": true}, SearchOpts{})
}

// SearchParts runs only some sources (gb, tw, ws = Workshop mirrors), so a
// window can show each as soon as it answers instead of waiting for the
// slowest site.
func SearchParts(q string, page int, parts map[string]bool, opt SearchOpts) SearchResults {
	useIndexCache()
	opt = opt.normalized(q)
	if opt.Period != "" {
		return searchTrending(q, page, parts, opt)
	}
	var r SearchResults
	if parts["s01"] {
		search01(&r, q, page, opt)
	}
	if parts["nx"] {
		searchNexus(&r, q, page, opt)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	addErr := func(e string) { mu.Lock(); r.Errors = append(r.Errors, e); mu.Unlock() }
	var sky []sources.SkyItem
	var tm []*sources.TMItem
	wg.Add(4)
	go func() {
		defer wg.Done()
		if !parts["tw"] && !parts["ws"] {
			return
		}
		sort := "popular" // True Workshop has no relevance order; its search filters
		if opt.Sort == "updated" {
			sort = "newest"
		}
		items, _, err := sources.TWSearch(q, sort, opt.Kind, (page-1)*24, 24)
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
		if !parts["gb"] {
			return
		}
		seedGBKinds()
		var gb []sources.GBMod
		var err error
		if opt.Kind == manager.KindMod && opt.Sort == "relevance" {
			gb, err = sources.GBSearch(q, page) // GameBanana's own relevance; filtered below
		} else {
			// Mods and contraptions are mixed in GameBanana's categories
			// (contraptions under Vehicles, Building...), so filter the
			// whole catalogue (about 560 uploads) by what each archive
			// holds; pages stay full.
			gb, err = gbCatalogue(opt.Kind, q, opt.Sort, page, 20)
		}
		if err != nil {
			addErr("GameBanana: " + err.Error())
			return
		}
		kinds := sources.GBKinds(gb)
		for _, m := range gb {
			if gbKind(m, kinds) != opt.Kind {
				continue // the other tab's, or not installable (skins, textures)
			}
			r.GameBanana = append(r.GameBanana, SearchResult{
				Ref: fmt.Sprintf("gb:%d", m.ID), Source: "GameBanana", Name: m.Name, Author: m.Submitter.Name,
				Category: m.Category.Name, Date: time.Unix(m.Modified, 0).Format("2006-01-02"), URL: m.URL, Image: m.Thumb(), Kind: opt.Kind,
			})
		}
	}()
	go func() {
		defer wg.Done()
		if !parts["ws"] || opt.Kind == manager.KindContraption {
			return
		}
		if opt.Sort == "popular" && strings.TrimSpace(q) == "" {
			return // Skymods has no popularity order; top-mods lists by downloads
		}
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
		if !parts["ws"] || opt.Kind == manager.KindContraption {
			return
		}
		var list []sources.TMSummary
		var err error
		if strings.TrimSpace(q) == "" && opt.Sort == "popular" {
			list, err = sources.TMTop(page)
		} else if strings.TrimSpace(q) == "" {
			list, err = sources.TMLatest(page)
		} else if list, err = sources.TMFind(q, page*12); err == nil {
			if len(list) > (page-1)*12 {
				list = list[(page-1)*12:]
			} else {
				list = nil
			}
		} else {
			list, err = sources.TMSearch(q, page) // sitemap unavailable
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
		ci := copyState(mr.ID)
		v := bestVersion(mr.ID, mr.Title)
		label := mr.Source
		if v != "" {
			label += " v" + v
		}
		label += " " + mr.Version
		if ci.gone || ci.invalid != "" {
			e.Mirrors = append(e.Mirrors, label+" (unavailable)")
			return // a copy known to be gone or broken does not decide the card
		}
		e.Mirrors = append(e.Mirrors, label)
		if !mr.AfterCutoff {
			e.AfterCutoff = false
		}
		// The card shows the copy with the highest version (by mod.json when
		// known, else by title); without versions, the newest revision.
		better := false
		switch c := CompareVersions(v, e.bestVer); {
		case !e.hasVer:
			better = true
		case c != 0:
			better = c > 0
		default:
			better = mr.VersionTime.After(e.newest)
		}
		if better {
			e.hasVer, e.bestVer = true, v
			e.Name, e.newest, e.Date, e.Size = mr.Title, mr.VersionTime, mr.Version, mr.Size
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
	// A True Workshop upload of a Workshop item found here joins its card
	// instead of showing up twice.
	var keepTW []SearchResult
	for _, t := range r.TrueWS {
		merged := false
		if t.Kind != "contraption" {
			for _, ws := range order {
				e := byWS[ws]
				if sameMod(t.Name, t.Author, e.Name, e.Author) {
					id := "trueworkshop:" + strings.TrimPrefix(t.Ref, "tw:")
					v := bestVersion(id, t.Name)
					label := "True Workshop"
					if v != "" {
						label += " v" + v
					}
					if t.Reviewed {
						label += " (reviewed)"
					}
					e.Mirrors = append(e.Mirrors, label)
					if v != "" && CompareVersions(v, e.bestVer) > 0 {
						// The upload is a newer version than the mirrors: show it.
						e.bestVer, e.Name, e.Date, e.Size = v, t.Name, t.Date, t.Size
						if e.Author == "" {
							e.Author = t.Author
						}
						e.AfterCutoff = false
					}
					merged = true
					break
				}
			}
		}
		if merged {
			r.MergedTW = append(r.MergedTW, t.Ref)
		} else {
			keepTW = append(keepTW, t)
		}
	}
	r.TrueWS = keepTW
	if !parts["tw"] {
		r.TrueWS = nil
	}
	if parts["ws"] && opt.Kind == manager.KindContraption {
		r.Notes = append(r.Notes, "The Workshop mirrors (Skymods, top-mods) only carry mods, not contraptions.")
	}
	if parts["ws"] && opt.Sort == "popular" && strings.TrimSpace(q) != "" && opt.Kind != manager.KindContraption {
		r.Notes = append(r.Notes, "Workshop mirror results stay in search order: the mirrors don't publish download counts for searches.")
	}
	for _, ws := range order {
		e := byWS[ws]
		if e.Author == "" {
			e.Author = knownAuthor(ws) // from a copy's mod.json
		}
		e.Version = e.bestVer
		r.Workshop = append(r.Workshop, *e)
	}
	if opt.Sort == "updated" {
		sort.SliceStable(r.Workshop, func(i, j int) bool { return r.Workshop[i].newest.After(r.Workshop[j].newest) })
	}
	if opt.Sort == "relevance" {
		r.Workshop = rerank(r.Workshop, q, func(SearchResult) int { return 0 })
		r.TrueWS = rerank(r.TrueWS, q, func(x SearchResult) int { return x.Downloads })
	}
	return r
}

// s01Result is a search card for a 01 STUDIO mod. Its ref is the Workshop
// item, so installing uses every copy (and 01 STUDIO's own via the browser).
func s01Result(m sources.S01Mod) SearchResult {
	r := SearchResult{Ref: "sky:" + m.WorkshopID(), Source: "01 STUDIO", Name: m.Title, Author: "01 STUDIO",
		Category: m.Category, Date: FmtTime(m.CreatedTime()), URL: m.Page(), Image: m.Image(), Kind: manager.KindMod,
		Mirrors: []string{"01 STUDIO"}}
	if m.Version != "" {
		r.Version = m.Version
		r.Mirrors = []string{"01 STUDIO v" + m.Version}
	}
	return r
}

// search01 lists 01 STUDIO's catalogue: most viewed first for relevance and
// popularity, newest first for "updated".
func search01(r *SearchResults, q string, page int, opt SearchOpts) {
	if opt.Kind == manager.KindContraption {
		r.Notes = append(r.Notes, "01 STUDIO publishes mods, not contraptions.")
		return
	}
	all, err := sources.S01All()
	if err != nil {
		r.Errors = append(r.Errors, "01 STUDIO: "+err.Error())
		return
	}
	var list []sources.S01Mod
	for _, m := range all {
		if m.WorkshopID() != "" {
			list = append(list, m)
		}
	}
	list = byRelevance(list, q, func(m sources.S01Mod) string { return m.Title }, func(sources.S01Mod) string { return "01 STUDIO" },
		func(m sources.S01Mod) int { return m.Views })
	if opt.Sort == "updated" {
		sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedTime().After(list[j].CreatedTime()) })
	} else if opt.Sort == "popular" || strings.TrimSpace(q) == "" {
		sort.SliceStable(list, func(i, j int) bool { return list[i].Views > list[j].Views })
	}
	const per = 24
	for i := (page - 1) * per; i < len(list) && i < page*per; i++ {
		r.Studio01 = append(r.Studio01, s01Result(list[i]))
	}
}

// NXResult is a search card for a Nexus Mods entry.
func NXResult(m sources.NXMod, kind string) SearchResult {
	return SearchResult{Ref: fmt.Sprintf("nx:%d", m.ID), Source: "Nexus Mods", Name: m.Name, Author: m.Author,
		Date: FmtTime(m.UpdatedTime()), URL: m.Page(), Image: m.Thumbnail, Kind: kind, Version: m.Version,
		Downloads: m.Downloads}
}

// NXKind is what a Nexus upload is, from its file's contents; unknown (no
// content preview) counts as a mod.
func NXKind(m sources.NXMod, infos map[int]sources.NXInfo) string {
	if infos == nil {
		infos = sources.NXInfos([]sources.NXMod{m})
	}
	if k := infos[m.ID].Kind; k != "" {
		return k
	}
	return manager.KindMod
}

// searchNexus lists Nexus Mods' People Playground section: most downloaded
// first for relevance and popularity, last updated first for "updated".
func searchNexus(r *SearchResults, q string, page int, opt SearchOpts) {
	all, err := sources.NXAll()
	if err != nil {
		r.Errors = append(r.Errors, "Nexus Mods: "+err.Error())
		return
	}
	// Nexus files everything under one category: tell mods from
	// contraptions by what each upload holds.
	infos := sources.NXInfos(all)
	var list []sources.NXMod
	for _, m := range all {
		if NXKind(m, infos) == opt.Kind {
			list = append(list, m)
		}
	}
	list = byRelevance(list, q, func(m sources.NXMod) string { return m.Name }, func(m sources.NXMod) string { return m.Author },
		func(m sources.NXMod) int { return m.Downloads })
	if opt.Sort == "updated" {
		sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedTime().After(list[j].UpdatedTime()) })
	} else if opt.Sort == "popular" || strings.TrimSpace(q) == "" {
		sort.SliceStable(list, func(i, j int) bool { return list[i].Downloads > list[j].Downloads })
	}
	const per = 24
	for i := (page - 1) * per; i < len(list) && i < page*per; i++ {
		r.Nexus = append(r.Nexus, NXResult(list[i], NXKind(list[i], infos)))
	}
}

// relevance scores how well a name (and author) matches a search: the whole
// name, the name's start, every word as a word of the name, every word
// inside the name, or only through the author. 0 means no match.
func relevance(q, name, author string) int {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return 1
	}
	n, a := normTitle(name), normTitle(author)
	nq := normTitle(q)
	nameWords := map[string]bool{}
	for _, w := range sources.SlugWords(name) {
		nameWords[w] = true
	}
	whole, inName, inAuthor := 0, 0, 0
	for _, w := range sources.SlugWords(q) {
		switch {
		case nameWords[w]:
			whole++
			inName++
		case strings.Contains(n, w):
			inName++
		case strings.Contains(a, w):
			inAuthor++
		default:
			return 0 // every word must match somewhere
		}
	}
	total := whole + (inName - whole) + inAuthor
	switch {
	case total == 0:
		return 0
	case n == nq:
		return 1000
	case strings.HasPrefix(strings.Join(sources.SlugWords(name), " ")+" ", strings.Join(sources.SlugWords(q), " ")+" "):
		return 800 // starts with the search, word for word
	case whole == total:
		return 600 - len(n) // fewer extra words first
	case inName == total:
		return 400 - len(n)
	}
	return 100 + 10*inName
}

// byRelevance orders items for a search: best match first, then by
// popularity (pop) for equal matches. Items that don't match are dropped.
func byRelevance[T any](items []T, q string, name, author func(T) string, pop func(T) int) []T {
	type scored struct {
		it    T
		score int
	}
	var s []scored
	for _, it := range items {
		if sc := relevance(q, name(it), author(it)); sc > 0 {
			s = append(s, scored{it, sc})
		}
	}
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].score != s[j].score {
			return s[i].score > s[j].score
		}
		return pop(s[i].it) > pop(s[j].it)
	})
	out := make([]T, len(s))
	for i := range s {
		out[i] = s[i].it
	}
	return out
}

// rerank puts a site's own search results in relevance order, keeping the
// ones the name alone doesn't explain (the site matched tags or text) last.
func rerank(list []SearchResult, q string, pop func(SearchResult) int) []SearchResult {
	if strings.TrimSpace(q) == "" {
		return list
	}
	ranked := byRelevance(list, q, func(r SearchResult) string { return r.Name }, func(r SearchResult) string { return r.Author }, pop)
	seen := map[string]bool{}
	for _, r := range ranked {
		seen[r.Ref] = true
	}
	for _, r := range list {
		if !seen[r.Ref] {
			ranked = append(ranked, r)
		}
	}
	return ranked
}

// gbKind is what a GameBanana upload is: from its archive's contents when
// known, else from its category.
func gbKind(m sources.GBMod, kinds map[int]string) string {
	if k := kinds[m.ID]; k != "" {
		return k
	}
	if m.Category.Name == "Contraptions" {
		return manager.KindContraption
	}
	return manager.KindMod
}

// gbCatalogue lists GameBanana uploads of one kind, wherever they are filed:
// newest change first, or most viewed first.
func gbCatalogue(kind, q, sortBy string, page, per int) ([]sources.GBMod, error) {
	all, err := sources.GBAll()
	if err != nil {
		return nil, err
	}
	kinds := sources.GBKinds(all)
	var out []sources.GBMod
	for _, m := range all {
		if gbKind(m, kinds) == kind {
			out = append(out, m)
		}
	}
	out = byRelevance(out, q, func(m sources.GBMod) string { return m.Name }, func(m sources.GBMod) string { return m.Submitter.Name },
		func(m sources.GBMod) int { return m.Views })
	switch {
	case sortBy == "updated":
		sort.SliceStable(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	case sortBy == "popular" || strings.TrimSpace(q) == "":
		sort.SliceStable(out, func(i, j int) bool { return out[i].Views > out[j].Views })
	}
	if (page-1)*per >= len(out) {
		return nil, nil
	}
	out = out[(page-1)*per:]
	if len(out) > per {
		out = out[:per]
	}
	return out, nil
}

var (
	gbSeedMu sync.Mutex
	gbSeeded time.Time
)

// seedGBKinds takes the kinds the daily index classified for every
// GameBanana upload, so this computer doesn't have to ask about all of them.
func seedGBKinds() {
	gbSeedMu.Lock()
	defer gbSeedMu.Unlock()
	if time.Since(gbSeeded) < time.Hour {
		return
	}
	gbSeeded = time.Now()
	ix, err := popularity.Fetch()
	if err != nil {
		return
	}
	kinds := map[int]string{}
	for _, it := range ix.Items {
		var id int
		if it.Src == "gb" && it.KindChecked {
			if _, err := fmt.Sscanf(it.Ref, "gb:%d", &id); err == nil {
				kinds[id] = it.Kind
			}
		}
	}
	sources.SeedGBKinds(kinds)
}

// searchTrending ranks by what gained the most views or downloads in the
// period, from the daily snapshots published by the popularity Action.
func searchTrending(q string, page int, parts map[string]bool, opt SearchOpts) SearchResults {
	var r SearchResults
	ix, err := popularity.Fetch()
	if err != nil {
		r.Errors = append(r.Errors, "Popularity data: "+err.Error()+"; showing all-time popularity instead")
		opt.Period = ""
		return SearchParts(q, page, parts, opt)
	}
	label := map[string]string{"day": "today", "week": "this week", "month": "this month"}[opt.Period]
	if days := ix.Days(); days == 0 {
		r.Notes = append(r.Notes, "Popularity tracking started today, so there is nothing to compare yet: cards are ranked by all-time counts until tomorrow.")
	} else if days < popularity.Periods[opt.Period] {
		r.Notes = append(r.Notes, fmt.Sprintf("Popularity tracking started %s, so \"%s\" covers the last %d day(s) for now.", ix.Since, label, days))
	}
	conv := func(it popularity.Item) SearchResult {
		res := SearchResult{Ref: it.Ref, Name: it.Name, Author: it.Author, Category: it.Category, Date: YMD(it.Date), URL: it.URL,
			Image: it.Image, Reviewed: it.Reviewed, Kind: it.Kind}
		if g := it.Gain(opt.Period); g > 0 {
			res.Trend = fmt.Sprintf("+%s %s %s", thousands(g), ix.Metric[it.Src], label)
		} else {
			res.Trend = fmt.Sprintf("%s %s all time", thousands(it.N), ix.Metric[it.Src])
		}
		switch it.Src {
		case "gb":
			res.Source = "GameBanana"
		case "tw":
			res.Source = "True Workshop"
		default:
			res.Source = "Steam Workshop"
			res.Date = YMD(it.Date)
			res.Mirrors = []string{"top-mods " + res.Date}
		}
		return res
	}
	const per = 24
	for _, src := range []string{"gb", "tw", "tm", "s01", "nx"} {
		part := map[string]string{"gb": "gb", "tw": "tw", "tm": "ws", "s01": "s01", "nx": "nx"}[src]
		if !parts[part] {
			continue
		}
		for _, it := range ix.Query(src, opt.Kind, q, opt.Period, (page-1)*per, per) {
			res := conv(it)
			switch src {
			case "gb":
				r.GameBanana = append(r.GameBanana, res)
			case "tw":
				r.TrueWS = append(r.TrueWS, res)
			case "nx":
				res.Source = "Nexus Mods"
				r.Nexus = append(r.Nexus, res)
			case "s01":
				res.Ref = "sky:" + strings.TrimPrefix(it.Ref, "s01:")
				res.Source, res.Mirrors = "01 STUDIO", []string{"01 STUDIO"}
				r.Studio01 = append(r.Studio01, res)
			default:
				rememberTitle(strings.TrimPrefix(it.Ref, "sky:"), it.Name, it.URL)
				r.Workshop = append(r.Workshop, res)
			}
		}
	}
	if parts["ws"] && opt.Kind == manager.KindContraption {
		r.Notes = append(r.Notes, "The Workshop mirrors (Skymods, top-mods) only carry mods, not contraptions.")
	}
	return r
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// ---- install ----

var reGBURL = regexp.MustCompile(`gamebanana\.com/mods/(\d+)`)
var reNXURL = regexp.MustCompile(`nexusmods\.com/peopleplayground/mods/(\d+)`)
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
	if mm := reNXURL.FindStringSubmatch(ref); mm != nil {
		return "nx:" + mm[1]
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
	AnyFile    bool   `json:"any_file,omitempty"` // the file's name doesn't start with the Workshop ID (01 STUDIO, Nexus)
	Mirror     string `json:"mirror,omitempty"`
	Key        string `json:"key,omitempty"` // install under this ref instead of sky:<WorkshopID> (nx:<id>)
	Name       string `json:"name,omitempty"`
	Version    string `json:"version,omitempty"`
}

// nexusBrowser is the browser download of a Nexus Mods file (its files are
// given to signed-in users).
func nexusBrowser(it sources.NXMod, key string) *NeedsBrowser {
	return &NeedsBrowser{URL: it.FilesPage(), AnyFile: true, Mirror: fmt.Sprintf("nexus:%d", it.ID), Key: key, Name: it.Name, Version: it.Version,
		Reason: "Nexus Mods gives its files to signed-in users: download it on the mod's Files tab"}
}

func (e *NeedsBrowser) Error() string {
	return "modsbase.com did not give ppgmods a download link (" + e.Reason + ")"
}

// Install installs gb:<id>, sky:<workshop id>, a GameBanana URL or a Steam
// Workshop URL.
func (a *App) Install(m *manager.Manager, ref string) error {
	ref = NormalizeRef(ref)
	c, err := a.Fetch(m, ref, m.State.Find(ref))
	var nb *NeedsBrowser
	if errors.As(err, &nb) && a.Opt.BrowserFallback {
		c, err = a.viaBrowser(nb)
	}
	if err != nil || c == nil {
		return err
	}
	// Already installed under another ref (e.g. found by its Workshop ID
	// and now installed from True Workshop): replace that copy instead of
	// adding a second one next to it.
	if ex := m.State.Find(ref); ex != nil && ex.Key != c.Key {
		c.Aliases = append(c.Aliases, c.Key)
		for _, a := range ex.Aliases {
			if a != ex.Key {
				c.Aliases = append(c.Aliases, a)
			}
		}
		c.Key = ex.Key
	} else if ex := m.State.Find(c.Key); ex != nil && ex.Key != c.Key {
		c.Aliases = append(c.Aliases, c.Key)
		c.Key = ex.Key
	}
	if err := m.Install(c); err != nil {
		return err
	}
	installedThumb(m, c.Key)
	return nil
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
	case strings.HasPrefix(ref, "nx:"):
		id, err := strconv.Atoi(strings.TrimPrefix(ref, "nx:"))
		if err != nil {
			return nil, fmt.Errorf("bad Nexus Mods id %q", ref)
		}
		it, err := sources.NXGet(id)
		if err != nil {
			return nil, err
		}
		return nil, nexusBrowser(*it, ref)
	}
	return nil, fmt.Errorf("unknown reference %q; use gb:<id>, sky:<workshop id>, tw:<id>, nx:<id>, or a mod page link", ref)
}

var fetchLocks sync.Map

// CacheDir is where downloaded archives and their thumbnails are kept, so a
// preview and the install that follows download only once.
func CacheDir(key string) (string, error) {
	d, err := manager.ConfigDir()
	if err != nil {
		return "", err
	}
	name := strings.ReplaceAll(key, ":", "-")
	if !reCacheName.MatchString(name) || strings.Trim(name, ".") == "" {
		return "", fmt.Errorf("bad cache key %q", key)
	}
	p := filepath.Join(d, "cache", name)
	return p, os.MkdirAll(p, 0o755)
}

// reCacheName is what a cache folder name may contain: refs like gb-123,
// sky-123, tw-12, local-m-some-folder. No separators, so no "..".
var reCacheName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,120}$`)

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
	prefix := nb.WorkshopID
	if nb.AnyFile {
		prefix = "" // any archive that appears from now on
		a.logf("Waiting for a new .zip/.rar/.7z in %s (up to %s)...", dl, a.Opt.Wait)
	} else {
		a.logf("Waiting for %s_* in %s (up to %s)...", nb.WorkshopID, dl, a.Opt.Wait)
	}
	file, err := waitForDownload(dl, prefix, since, a.Opt.Wait)
	if err != nil {
		return nil, fmt.Errorf("%v; when you have the file, import it with workshop id %s", err, nb.WorkshopID)
	}
	a.logf("got %s", filepath.Base(file))
	c, err := a.with(func(o *Options) { o.WorkshopID, o.Name = nb.WorkshopID, nb.Name }).candidate(file)
	if err == nil && c != nil && nb.Key != "" {
		c.Key = nb.Key
	}
	if err == nil && c != nil && nb.Version != "" {
		c.Version = nb.Version
	}
	if err == nil && c != nil && (strings.HasPrefix(nb.Mirror, "01studio:") || strings.HasPrefix(nb.Mirror, "nexus:")) {
		// From the author's own site, not a Steam copy: the worm-cutoff date
		// check doesn't apply (the scanner and the rest of the policy do).
		c.SteamOrig, c.Revision, c.Mirror, c.Source = false, time.Time{}, nb.Mirror, nb.URL
	}
	return c, err
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

// browserHosts are the sites ppgmods ever opens in the browser. Links come
// from scraped pages, so anything else (file:, other programs' URL schemes,
// a link to an .exe on some other host) is refused.
var browserHosts = []string{"gamebanana.com", "steamcommunity.com", "github.com", "catalogue.smods.ru",
	"top-mods.com", "ppgworkshop.onrender.com", "modsbase.com", "modsfire.com", "01studio.dev", "nexusmods.com"}

// AllowedURL reports whether ppgmods may open u in the browser: https on one
// of browserHosts (or a subdomain), or this app's own window on 127.0.0.1.
func AllowedURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.User != nil || p.Opaque != "" {
		return false
	}
	host := strings.ToLower(p.Hostname())
	if p.Scheme == "http" && host == "127.0.0.1" {
		return true
	}
	if p.Scheme != "https" || p.Port() != "" {
		return false
	}
	for _, h := range browserHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// OpenBrowser opens u in the default browser, if AllowedURL permits it.
func OpenBrowser(u string) error {
	if !AllowedURL(u) {
		return fmt.Errorf("refusing to open %q: not a known mod site", u)
	}
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
	if err := m.Install(c); err != nil {
		return err
	}
	installedThumb(m, c.Key)
	return nil
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

// Repair reinstalls installed items whose folders are missing or whose files
// were changed: the same GameBanana file or mirror copy as before, through
// every safety check again. Items found on this PC without a source (local:)
// can't be downloaded again.
func (a *App) Repair(m *manager.Manager, keys []string) Summary {
	var s Summary
	for _, k := range keys {
		inst := m.State.Mods[k]
		if inst == nil {
			continue
		}
		a.logf("== restoring %s (%s)", inst.Name, k)
		if strings.HasPrefix(k, "local:") {
			a.logf("  %s was installed from a file on this PC, not a mod site; import that file again", inst.Name)
			s.Failed++
			continue
		}
		b := a.with(func(o *Options) { o.Mirror, o.FileID = inst.Mirror, inst.FileID })
		if strings.HasPrefix(inst.Mirror, "01studio:") {
			b.Opt.Mirror = "" // needs the browser; any mirror copy will do
		}
		a.tally(&s, k, "REFUSED", b.Install(m, k))
	}
	a.logf("restore finished: %d restored, %d refused, %d failed", s.OK, s.Refused, s.Failed)
	return s
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
			if err == nil && inst.Adopted && inst.ArchiveSHA == "" {
				// Found already installed: we never saw its archive, so compare
				// mod.json versions instead of file checksums.
				if ver := inspect(m, c).version; CompareVersions(ver, inst.Version) <= 0 {
					inst.ArchiveSHA = c.ArchiveSHA
					m.State.Save()
					s.Current++
					continue
				}
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
		dest = filepath.Join(dir, "workshop-backup-"+time.Now().Format("2006-01-02"))
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
