package sources

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const skyBase = "https://catalogue.smods.ru"
const PPGAppID = 1118200

type SkyItem struct {
	Title       string    `json:"title"`
	PageURL     string    `json:"page_url"`
	DownloadURL string    `json:"download_url"` // modsbase.com page, opened in a browser
	WorkshopID  string    `json:"workshop_id"`
	Author      string    `json:"author"`
	Revision    time.Time `json:"revision"` // Steam "last revision" of the mirrored copy
	RevisionRaw string    `json:"revision_raw"`
	Mirrored    time.Time `json:"mirrored"`
	Size        string    `json:"size"`
	Image       string    `json:"image"`
	Tags        []string  `json:"tags,omitempty"` // Steam Workshop tags, e.g. "mods", "building", "vehicles"
}

// Contraption reports what the Workshop tags say: People Playground tags
// every mod upload "Mods"; contraptions only carry their subject tags
// (Building, Vehicles, Destructible...).
func (it SkyItem) Contraption() bool {
	if len(it.Tags) == 0 {
		return false // unknown
	}
	for _, t := range it.Tags {
		if t == "mods" {
			return false
		}
	}
	return true
}

var reSkyTag = regexp.MustCompile(`/category/([a-z0-9-]+)\?app=`)

var (
	reArticle  = regexp.MustCompile(`(?s)<article\b.*?</article>`)
	reTitle    = regexp.MustCompile(`title="Permalink to ([^"]*)"`)
	rePage     = regexp.MustCompile(`href="(https://catalogue\.smods\.ru/archives/\d+)"`)
	reDL       = regexp.MustCompile(`href="(https://modsbase\.com/[^"]+)"`)
	reWS       = regexp.MustCompile(`filedetails/\?id=(\d+)`)
	reAuthor   = regexp.MustCompile(`Author: <a [^>]*>([^<]*)</a>`)
	reRevision = regexp.MustCompile(`skymods-item-date">([^<]*)<`)
	reMirrored = regexp.MustCompile(`datetime="(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d)"`)
	reSize     = regexp.MustCompile(`skymods-item-file-size"[^>]*>([^<]*)<`)
	reAppCat   = regexp.MustCompile(`\?app=(\d+)`)
	reImage    = regexp.MustCompile(`<img src="(https://[^"?]+)`)
)

// SkySearch searches the Skymods People Playground catalogue. Searching for a
// Workshop ID finds the mirrored copy of that item.
func SkySearch(query string, page int) ([]SkyItem, error) {
	u := skyBase + "/"
	if page > 1 {
		u += fmt.Sprintf("page/%d/", page)
	}
	u += "?s=" + url.QueryEscape(query) + "&app=" + strconv.Itoa(PPGAppID)
	return skyList(u)
}

// SkyLatest lists the newest catalogue entries.
func SkyLatest(page int) ([]SkyItem, error) {
	u := skyBase + "/game/people-playground/"
	if page > 1 {
		u += fmt.Sprintf("page/%d/", page)
	}
	return skyList(u)
}

// SkyCopies returns every mirrored copy of a Workshop item (Skymods sometimes
// lists an item more than once), newest revision first.
func SkyCopies(id string) ([]SkyItem, error) {
	items, err := SkySearch(id, 1)
	if err != nil {
		return nil, err
	}
	var out []SkyItem
	for _, it := range items {
		if it.WorkshopID == id {
			out = append(out, it)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("workshop item %s not found on Skymods", id)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Revision.After(out[j].Revision) })
	return out, nil
}

// SkyByWorkshopID returns the mirrored copy of a Workshop item, if any.
func SkyByWorkshopID(id string) (*SkyItem, error) {
	items, err := SkySearch(id, 1)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.WorkshopID == id {
			return &it, nil
		}
	}
	return nil, fmt.Errorf("workshop item %s not found on Skymods", id)
}

// ErrSkyChallenge means catalogue.smods.ru answered with a Cloudflare check
// instead of the page: no plain HTTP client can get past it, so ppgmods does
// not try. It usually clears on its own; installs can still come from the
// other mirrors meanwhile. A clearance the person's browser earned (see the
// cfclear package) is attached automatically when one is stored.
var ErrSkyChallenge = errors.New("smods.ru is showing a Cloudflare browser check right now; try again later")

// SkyClearance, if set, returns the cf_clearance cookie the person's browser
// earned from smods.ru and the User-Agent it was issued to (Cloudflare binds
// the cookie to both). Empty strings mean none is stored.
// Set by the cfclear package; only ever sent to smods.ru hosts.
var SkyClearance func() (cookie, userAgent string)

// SkyClearanceExpired, if set, is called when Cloudflare challenges even
// with a stored clearance attached: the cookie no longer works and the
// check has to be passed again.
var SkyClearanceExpired func()

// SkyAutoCheck, if set, passes the check automatically with a background
// browser (the cfclear package) and stores a fresh clearance. It reports
// whether the site is usable now; callers retry their request when it is.
var SkyAutoCheck func() bool

var (
	skyChallengeMu sync.Mutex
	skyChallengeAt time.Time // the last time a check was served
)

// SkyChallengeUp reports whether a Cloudflare check was seen recently,
// i.e. the catalogue is blocked for ppgmods right now.
func SkyChallengeUp() bool {
	skyChallengeMu.Lock()
	defer skyChallengeMu.Unlock()
	return time.Since(skyChallengeAt) < 15*time.Minute
}

func markSkyChallenge() {
	skyChallengeMu.Lock()
	skyChallengeAt = time.Now()
	skyChallengeMu.Unlock()
}

func clearSkyChallenge() {
	skyChallengeMu.Lock()
	skyChallengeAt = time.Time{}
	skyChallengeMu.Unlock()
}

// InvalidateSkyCache drops cached Skymods pages and the challenge marker,
// so the next request goes out fresh (used when a clearance is saved).
func InvalidateSkyCache() {
	skyCacheMu.Lock()
	skyCache = map[string]skyCached{}
	skyCacheMu.Unlock()
	clearSkyChallenge()
}

var (
	skyCacheMu sync.Mutex
	skyCache   = map[string]skyCached{}
)

type skyCached struct {
	items []SkyItem
	at    time.Time
	err   error // a cached Cloudflare challenge: don't ask again for a while
}

// skyList fetches a catalogue page. Skymods can take 10-20 s to answer, so
// pages are kept for 10 minutes; a challenge is kept just as long, so a
// blocked catalogue is not asked again on every search.
func skyList(u string) ([]SkyItem, error) {
	skyCacheMu.Lock()
	if c, ok := skyCache[u]; ok && time.Since(c.at) < 10*time.Minute {
		skyCacheMu.Unlock()
		return c.items, c.err
	}
	skyCacheMu.Unlock()
	items, err := skyFetch(u)
	if err == nil || errors.Is(err, ErrSkyChallenge) {
		skyCacheMu.Lock()
		skyCache[u] = skyCached{items, time.Now(), err}
		skyCacheMu.Unlock()
	}
	return items, err
}

func skyFetch(u string) ([]SkyItem, error) {
	b, err := skyGet(u)
	if err != nil {
		return nil, err
	}
	return ParseSkyPage(string(b)), nil
}

// skyGet fetches a Skymods page, telling a Cloudflare challenge from other
// failures (get() would report both as a bare HTTP status). When a check is
// served it heals on its own: a stored clearance (the person's browser or a
// background browser passed the check earlier) is attached and the request
// retried; if none is stored, a background browser passes the check by
// itself. Every caller here serves catalogue.smods.ru, so the clearance
// never goes anywhere else.
func skyGet(u string) ([]byte, error) {
	b, challenge, err := skyGetOnce(u, "", "")
	if err != nil {
		return nil, err
	}
	for challenge {
		if SkyClearance != nil {
			if cookie, userAgent := SkyClearance(); cookie != "" {
				b, challenge, err = skyGetOnce(u, cookie, userAgent)
				if err != nil {
					return nil, err
				}
				if challenge && SkyClearanceExpired != nil {
					SkyClearanceExpired() // this clearance no longer works
				}
			}
		}
		if !challenge {
			break
		}
		// Nothing stored (or it was rejected): pass the check with a
		// background browser, then try the page again with what it stored.
		if SkyAutoCheck == nil || !SkyAutoCheck() {
			return nil, ErrSkyChallenge
		}
		cookie, userAgent := "", ""
		if SkyClearance != nil {
			cookie, userAgent = SkyClearance()
		}
		b, challenge, err = skyGetOnce(u, cookie, userAgent)
		if err != nil {
			return nil, err
		}
		if challenge && SkyClearanceExpired != nil {
			SkyClearanceExpired()
		}
	}
	return b, nil
}

func skyGetOnce(u, clearance, userAgent string) (b []byte, challenge bool, err error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, false, err
	}
	setHeaders(req)
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent) // the clearance is bound to this UA
	}
	if clearance != "" {
		req.Header.Add("Cookie", "cf_clearance="+clearance)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	b, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false, err
	}
	if isChallenge(resp, b) {
		markSkyChallenge()
		return nil, true, nil
	}
	if resp.StatusCode != 200 {
		return nil, false, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	clearSkyChallenge()
	return b, false, nil
}

// ParseSkyPage extracts catalogue entries from a Skymods listing page.
func ParseSkyPage(page string) []SkyItem {
	var items []SkyItem
	for _, a := range reArticle.FindAllString(page, -1) {
		if m := reAppCat.FindStringSubmatch(a); m != nil && m[1] != strconv.Itoa(PPGAppID) {
			continue
		}
		it := SkyItem{
			Title:       html.UnescapeString(first(reTitle, a)),
			PageURL:     first(rePage, a),
			DownloadURL: html.UnescapeString(first(reDL, a)),
			WorkshopID:  first(reWS, a),
			Author:      html.UnescapeString(first(reAuthor, a)),
			RevisionRaw: strings.TrimSpace(first(reRevision, a)),
			Size:        strings.TrimSpace(first(reSize, a)),
			Image:       first(reImage, a),
		}
		if t, err := time.Parse("2006-01-02 15:04:05", first(reMirrored, a)); err == nil {
			it.Mirrored = t.UTC()
		}
		it.Revision = ParseSteamDate(it.RevisionRaw, it.Mirrored)
		seen := map[string]bool{}
		for _, m := range reSkyTag.FindAllStringSubmatch(a, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				it.Tags = append(it.Tags, m[1])
			}
		}
		if it.WorkshopID != "" {
			items = append(items, it)
		}
	}
	return items
}

func first(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// ParseSteamDate parses Steam's "2 Aug, 2025 at 04:15 UTC" format. Steam drops
// the year for dates in the current year ("20 Sep at 12:04 UTC"); ref (the
// mirror date) supplies it. Returns the zero time if unparseable.
func ParseSteamDate(s string, ref time.Time) time.Time {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "UTC"))
	for _, layout := range []string{"2 Jan, 2006 at 15:04", "2 Jan, 2006 @ 3:04pm"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	for _, layout := range []string{"2 Jan at 15:04", "2 Jan @ 3:04pm"} {
		if t, err := time.Parse(layout, s); err == nil {
			y := ref.Year()
			if ref.IsZero() {
				y = time.Now().UTC().Year()
			}
			t = t.AddDate(y, 0, 0)
			if !ref.IsZero() && t.After(ref.Add(24*time.Hour)) {
				t = t.AddDate(-1, 0, 0)
			}
			return t.UTC()
		}
	}
	return time.Time{}
}
