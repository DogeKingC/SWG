package sources

import (
	"fmt"
	"html"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
}

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

func skyList(u string) ([]SkyItem, error) {
	resp, err := get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return ParseSkyPage(string(b)), nil
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
