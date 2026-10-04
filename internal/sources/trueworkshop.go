package sources

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// True Workshop (ppgworkshop.onrender.com) is a community archive of
// uploaded PPG mods. Uploads pass the site's scanner and most are reviewed
// by its maintainers ("trust": "dev"). The files are on a Hugging Face
// dataset and each item lists the file's SHA-256.

const twBase = "https://ppgworkshop.onrender.com"

type TWItem struct {
	ID        int      `json:"id"`
	UUID      string   `json:"uuid"`
	Title     string   `json:"title"`
	Author    string   `json:"author"`
	Tags      []string `json:"tags"`
	Type      string   `json:"item_type"` // mod or contraption
	FileURL   string   `json:"file_url"`
	Preview   string   `json:"preview_url"`
	Size      int64    `json:"file_size_bytes"`
	SHA256    string   `json:"sha256"`
	Downloads int      `json:"downloads_count"`
	Likes     int      `json:"likes_count"`
	Scan      string   `json:"scan_status"`
	Trust     string   `json:"trust"` // "dev" = reviewed by the maintainers
	Created   string   `json:"created_at"`
}

// Reviewed reports whether the site's maintainers reviewed the upload (as
// opposed to it only passing the site's automated scanner).
func (it TWItem) Reviewed() bool { return it.Trust == "dev" }

func (it TWItem) CreatedTime() time.Time {
	t, _ := time.Parse("2006-01-02 15:04:05", it.Created)
	return t.UTC()
}

func (it TWItem) Page() string { return twBase + "/#item-" + strconv.Itoa(it.ID) }

// Thumb is the preview image; the site serves its own copy too.
func (it TWItem) Thumb() string {
	if it.Preview != "" {
		return escapeURL(it.Preview)
	}
	return fmt.Sprintf("%s/api/preview/%d", twBase, it.ID)
}

// escapeURL percent-encodes spaces and other characters in a URL path
// (Hugging Face file names contain spaces).
func escapeURL(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	return p.String()
}

// DownloadURL is the file on Hugging Face, properly escaped.
func (it TWItem) DownloadURL() string { return escapeURL(it.FileURL) }

var twClient = &http.Client{Timeout: 90 * time.Second} // free hosting: the first request may wake the server

func twGet(u string, v any) (int, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return 0, err
	}
	setHeaders(req)
	resp, err := twClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("True Workshop: HTTP %d", resp.StatusCode)
	}
	total, _ := strconv.Atoi(resp.Header.Get("X-Total-Count"))
	return total, json.NewDecoder(resp.Body).Decode(v)
}

// TWSearch lists True Workshop items (sort: popular, likes or newest; kind:
// "mod", "contraption" or "" for both).
func TWSearch(query, sort, kind string, offset, limit int) ([]TWItem, int, error) {
	if sort == "" {
		sort = "popular"
	}
	q := url.Values{"sort": {sort}, "limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}
	if kind != "" {
		q.Set("type", kind)
	}
	if s := strings.TrimSpace(query); s != "" {
		q.Set("search", s)
	}
	var items []TWItem
	total, err := twGet(twBase+"/api/items?"+q.Encode(), &items)
	return items, total, err
}

var (
	twAllMu sync.Mutex
	twAll   []TWItem
	twAllAt time.Time
)

// TWAll returns the whole catalogue (cached for 10 minutes); it is small.
func TWAll() ([]TWItem, error) {
	twAllMu.Lock()
	defer twAllMu.Unlock()
	if twAll != nil && time.Since(twAllAt) < 10*time.Minute {
		return twAll, nil
	}
	var all []TWItem
	for off := 0; off < 5000; off += 100 {
		var page []TWItem
		q := url.Values{"sort": {"newest"}, "limit": {"100"}, "offset": {strconv.Itoa(off)}}
		if _, err := twGet(twBase+"/api/items?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < 100 {
			break
		}
	}
	twAll, twAllAt = all, time.Now()
	return all, nil
}

// TWGet returns one item by id.
func TWGet(id int) (*TWItem, error) {
	all, err := TWAll()
	if err != nil {
		return nil, err
	}
	for _, it := range all {
		if it.ID == id {
			return &it, nil
		}
	}
	return nil, fmt.Errorf("True Workshop item %d not found (it may have been removed)", id)
}

// TWTrackDownload tells the site a download happened, as its own download
// button does. Failures are ignored.
func TWTrackDownload(id int) {
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/track-download/%d", twBase, id), nil)
	if err != nil {
		return
	}
	setHeaders(req)
	if resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req); err == nil {
		resp.Body.Close()
	}
}
