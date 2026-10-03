package sources

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// top-mods.com is a second Steam Workshop mirror. It often has a newer
// revision than Skymods, mods Skymods never copied, and its own copies of
// the preview images. Files are on modsfire.com, with a modsbase.com
// "alternate link".

const tmBase = "https://top-mods.com"

type TMSummary struct {
	ID     string    `json:"id"` // numeric item id
	URL    string    `json:"url"`
	Title  string    `json:"title"`
	Image  string    `json:"image,omitempty"`
	Posted time.Time `json:"posted,omitempty"`
}

type TMItem struct {
	TMSummary
	WorkshopID  string    `json:"workshop_id"`
	Version     string    `json:"version"`      // as listed, e.g. "19.09.2026"
	VersionTime time.Time `json:"version_time"` // parsed Steam revision day, if it is a date
	Author      string    `json:"author"`
	Size        string    `json:"size"`
	Description string    `json:"description"`
	Downloads   []string  `json:"downloads"` // modsfire.com first, then modsbase.com
}

var (
	reTMID    = regexp.MustCompile(`/(\d+)-[^/]+\.html$`)
	reTMLink  = regexp.MustCompile(`href="(/mods/people-playground/[^"#]+\.html)"`)
	reTMImg   = regexp.MustCompile(`<img src="(/upload/[^"]+)"`)
	reTMHeadA = regexp.MustCompile(`(?s)<h[23][^>]*>\s*<a[^>]*>\s*(.*?)\s*</a>`)
	reTMTitle = regexp.MustCompile(`(?s)<h1[^>]*>(.*?)</h1>`)
	reTMField = func(name string) *regexp.Regexp {
		return regexp.MustCompile(name + `:\s*<span class="float-right">(?:<a[^>]*>)?([^<]*)`)
	}
	reTMVersion  = reTMField("Mod version")
	reTMAuthor   = reTMField("Author")
	reTMSize     = reTMField("File size")
	reTMSource   = regexp.MustCompile(`Source:\s*<span class="float-right"><a href="https://steamcommunity\.com/(?:sharedfiles|workshop)/filedetails/\?id=(\d+)"`)
	reTMPosted   = regexp.MustCompile(`<time[^>]*datetime="([^"]+)"`)
	reTMImage    = regexp.MustCompile(`<img src="(/upload/[^"]+-photo-big\.[a-z]+)"`)
	reTMDesc     = regexp.MustCompile(`(?s)<section id="content-tab1" class="value">(.*?)</section>`)
	reTMDownload = regexp.MustCompile(`href="(https://(?:modsfire\.com|modsbase\.com)/[^"]+)"`)
	reTitleSuff  = regexp.MustCompile(`(?i)\s+for People Playground\s*$`)
)

func tmText(s string) string { return strings.TrimSpace(html.UnescapeString(s)) }

func tmTitle(s string) string { return reTitleSuff.ReplaceAllString(tmText(s), "") }

func tmID(u string) string { return first(reTMID, u) }

func tmGet(u string) (string, error) {
	resp, err := get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), err
}

// TMSearch searches top-mods for People Playground mods. Its search covers
// every game and matches all words, so "People Playground" (which every PPG
// item title ends with) is added to the query.
func TMSearch(query string, page int) ([]TMSummary, error) {
	u := fmt.Sprintf("%s/search?q=%s", tmBase, url.QueryEscape(strings.TrimSpace(query)+" People Playground"))
	if page > 1 {
		u += fmt.Sprintf("&type=words&date=all&page=%d", page)
	}
	h, err := tmGet(u)
	if err != nil {
		return nil, err
	}
	return ParseTMSearch(h), nil
}

func ParseTMSearch(h string) []TMSummary {
	if i := strings.Index(h, `id="search_results_list"`); i >= 0 {
		h = h[i:]
	}
	return tmBlocks(h, `<div class="item">`)
}

// tmBlocks parses result blocks that start with marker: one People
// Playground link, an optional image, a title heading and a date.
func tmBlocks(h, marker string) []TMSummary {
	var out []TMSummary
	parts := strings.Split(h, marker)
	for _, b := range parts[1:] {
		link := first(reTMLink, b)
		if link == "" {
			continue
		}
		sm := TMSummary{URL: tmBase + link, ID: tmID(link), Title: tmTitle(HTMLText(first(reTMHeadA, b)))}
		if img := first(reTMImg, b); img != "" {
			sm.Image = tmBase + img
		}
		sm.Posted, _ = time.Parse(time.RFC3339, first(reTMPosted, b))
		if sm.ID != "" && sm.Title != "" {
			out = append(out, sm)
		}
	}
	return out
}

// TMLatest lists the newest People Playground items.
func TMLatest(page int) ([]TMSummary, error) {
	u := tmBase + "/mods/people-playground"
	if page > 1 {
		u += fmt.Sprintf("?page=%d", page)
	}
	h, err := tmGet(u)
	if err != nil {
		return nil, err
	}
	return tmBlocks(h, `<div class="content_list_item mods_list_item">`), nil
}

var (
	tmCacheMu sync.Mutex
	tmCache   = map[string]tmCached{}
)

type tmCached struct {
	item *TMItem
	at   time.Time
}

// TMDetails reads (and briefly caches) a top-mods item page.
func TMDetails(itemURL string) (*TMItem, error) {
	if !strings.HasPrefix(itemURL, tmBase+"/mods/people-playground/") {
		return nil, fmt.Errorf("not a top-mods People Playground page: %s", itemURL)
	}
	tmCacheMu.Lock()
	if c, ok := tmCache[itemURL]; ok && time.Since(c.at) < 30*time.Minute {
		tmCacheMu.Unlock()
		return c.item, nil
	}
	tmCacheMu.Unlock()
	h, err := tmGet(itemURL)
	if err != nil {
		return nil, err
	}
	it := ParseTMItem(h, itemURL)
	tmCacheMu.Lock()
	tmCache[itemURL] = tmCached{it, time.Now()}
	tmCacheMu.Unlock()
	return it, nil
}

func ParseTMItem(h, itemURL string) *TMItem {
	it := &TMItem{TMSummary: TMSummary{URL: itemURL, ID: tmID(itemURL)}}
	it.Title = tmTitle(HTMLText(first(reTMTitle, h)))
	it.Version = tmText(first(reTMVersion, h))
	it.VersionTime = ParseTMVersion(it.Version)
	it.Author = tmText(first(reTMAuthor, h))
	it.Size = tmText(first(reTMSize, h))
	it.WorkshopID = first(reTMSource, h)
	it.Posted, _ = time.Parse(time.RFC3339, first(reTMPosted, h))
	if img := first(reTMImage, h); img != "" {
		it.Image = tmBase + img
	}
	it.Description = HTMLText(first(reTMDesc, h))
	seen := map[string]bool{}
	for _, m := range reTMDownload.FindAllStringSubmatch(h, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			it.Downloads = append(it.Downloads, html.UnescapeString(m[1]))
		}
	}
	// modsfire first: it is top-mods' main link.
	for i := 1; i < len(it.Downloads); i++ {
		if strings.Contains(it.Downloads[i], "modsfire.com") && !strings.Contains(it.Downloads[0], "modsfire.com") {
			it.Downloads[0], it.Downloads[i] = it.Downloads[i], it.Downloads[0]
		}
	}
	return it
}

// ParseTMVersion reads top-mods' "Mod version", which is the Steam revision
// date as DD.MM.YY or DD.MM.YYYY. Other values return the zero time.
func ParseTMVersion(v string) time.Time {
	v = strings.TrimSpace(v)
	for _, layout := range []string{"02.01.2006", "2.1.2006", "02.01.06", "2.1.06"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ---- modsfire.com ----

var (
	reMFGenerate = regexp.MustCompile(`href="(/download/[A-Za-z0-9]+/[A-Za-z0-9]+)"`)
	reMFFile     = regexp.MustCompile(`href="(/d/[A-Za-z0-9]+)"`)
	reMFPage     = regexp.MustCompile(`^https://modsfire\.com/[A-Za-z0-9]+$`)
)

// ModsfireDownload follows modsfire's "Generate Link" → "Download" steps
// (the session cookie set by the first page is required) and saves the file.
func ModsfireDownload(page, referer, path string, maxBytes int64) (string, error) {
	if !reMFPage.MatchString(page) {
		return "", fmt.Errorf("not a modsfire file page: %s", page)
	}
	step := func(u, ref string) (*http.Response, error) {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		setHeaders(req)
		req.Header.Set("Referer", ref)
		return client.Do(req)
	}
	read := func(resp *http.Response) (string, error) {
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if isChallenge(resp, b) {
			return "", ErrChallenge
		}
		if resp.StatusCode == 404 {
			return "", ErrGone
		}
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("modsfire.com: HTTP %d", resp.StatusCode)
		}
		return string(b), err
	}
	resp, err := step(page, referer)
	if err != nil {
		return "", err
	}
	h, err := read(resp)
	if err != nil {
		return "", err
	}
	gen := first(reMFGenerate, h)
	if gen == "" {
		return "", errors.New("modsfire.com page has no download link")
	}
	time.Sleep(time.Second)
	if resp, err = step("https://modsfire.com"+gen, page); err != nil {
		return "", err
	}
	if h, err = read(resp); err != nil {
		return "", err
	}
	file := first(reMFFile, h)
	if file == "" {
		return "", errors.New("modsfire.com did not generate a download link")
	}
	if resp, err = step("https://modsfire.com"+file, "https://modsfire.com"+gen); err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if isChallenge(resp, head) {
			return "", ErrChallenge
		}
		if resp.StatusCode == 404 || strings.Contains(strings.ToLower(string(head)), "not found") {
			return "", ErrGone
		}
		return "", fmt.Errorf("modsfire.com returned a web page instead of the file (HTTP %d)", resp.StatusCode)
	}
	f, err := createFile(path)
	if err != nil {
		return "", err
	}
	_, sum, err := saveHashed(f, resp.Body, maxBytes)
	if err != nil {
		removeFile(path)
	}
	return sum, err
}

// TMItemID extracts the numeric id from a top-mods URL or "tm:<id>".
func TMItemID(s string) string {
	if strings.HasPrefix(s, "tm:") {
		if _, err := strconv.Atoi(s[3:]); err == nil {
			return s[3:]
		}
	}
	return tmID(s)
}

// ---- sitemap index ----
//
// top-mods' own search spans every game and needs every word to match, so
// ppgmods also reads the site's sitemap (every item URL, slug = title) and
// matches names locally. The sitemap is cached on disk for 12 hours.

// IndexCacheDir is where downloaded indexes are cached (set by the app).
var IndexCacheDir string

var (
	tmIndexMu sync.Mutex
	tmIndex   []TMSummary
	tmIndexAt time.Time
	reSMLoc   = regexp.MustCompile(`<loc>(https://top-mods\.com/mods/people-playground/[a-z0-9-]+/(\d+)-([^<]+)\.html)</loc>`)
	reSlugSep = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

// TMIndex returns every People Playground item listed in top-mods' sitemap.
func TMIndex() ([]TMSummary, error) {
	tmIndexMu.Lock()
	defer tmIndexMu.Unlock()
	if tmIndex != nil && time.Since(tmIndexAt) < 12*time.Hour {
		return tmIndex, nil
	}
	var body string
	cache := ""
	if IndexCacheDir != "" {
		cache = filepath.Join(IndexCacheDir, "topmods-sitemap.xml")
		if st, err := os.Stat(cache); err == nil && time.Since(st.ModTime()) < 12*time.Hour {
			if b, err := os.ReadFile(cache); err == nil {
				body = string(b)
			}
		}
	}
	if body == "" {
		var err error
		if body, err = tmGetLimit(tmBase+"/sitemap_content_mods.xml", 64<<20); err != nil {
			return nil, err
		}
		if cache != "" {
			os.MkdirAll(IndexCacheDir, 0o755)
			os.WriteFile(cache, []byte(body), 0o644)
		}
	}
	tmIndex = ParseTMSitemap(body)
	tmIndexAt = time.Now()
	return tmIndex, nil
}

func ParseTMSitemap(body string) []TMSummary {
	var out []TMSummary
	for _, m := range reSMLoc.FindAllStringSubmatch(body, -1) {
		out = append(out, TMSummary{URL: m[1], ID: m[2], Title: strings.ReplaceAll(m[3], "-", " ")})
	}
	return out
}

func tmGetLimit(u string, max int64) (string, error) {
	resp, err := get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, max))
	return string(b), err
}

// SlugWords lowercases a title into the words top-mods uses in its URLs.
func SlugWords(s string) []string {
	var out []string
	for _, w := range reSlugSep.Split(strings.ToLower(s), -1) {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// TMFind matches query words against every top-mods item name (from the
// sitemap). Items containing all words come first, exact names before
// longer ones, newest first.
func TMFind(query string, limit int) ([]TMSummary, error) {
	idx, err := TMIndex()
	if err != nil {
		return nil, err
	}
	q := SlugWords(query)
	if len(q) == 0 {
		return nil, nil
	}
	type hit struct {
		s     TMSummary
		extra int
		id    int
	}
	var hits []hit
	for _, s := range idx {
		words := SlugWords(s.Title)
		ok := true
		for _, w := range q {
			found := false
			for _, x := range words {
				if x == w || len(w) >= 3 && strings.HasPrefix(x, w) {
					found = true
					break
				}
			}
			if !found {
				ok = false
				break
			}
		}
		if ok {
			id, _ := strconv.Atoi(s.ID)
			hits = append(hits, hit{s, len(words) - len(q), id})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].extra != hits[j].extra {
			return hits[i].extra < hits[j].extra
		}
		return hits[i].id > hits[j].id
	})
	var out []TMSummary
	for i, h := range hits {
		if i >= limit {
			break
		}
		out = append(out, h.s)
	}
	return out, nil
}
