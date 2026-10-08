package sources

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 01 STUDIO (https://01studio.dev/mods) publishes its People Playground mods
// on its own site. The catalogue is public. Free mods can be downloaded by
// anyone from the site's CDN, by Workshop ID (S01DownloadURL); paid
// versions (Early Access, for subscribers) are left out. ppgmods lists only
// the free mods and links each to its Workshop ID.

const (
	s01API  = "https://api.01studio.dev"
	s01Site = "https://01studio.dev"
	s01CDN  = "https://cdn.01studio.dev/files/download/"
)

// S01DownloadURL is where 01 STUDIO serves the free file of a mod, by the
// mod's Steam Workshop ID; "" for anything that isn't one.
func S01DownloadURL(ws string) string {
	if len(ws) < 6 || len(ws) > 12 || strings.Trim(ws, "0123456789") != "" {
		return ""
	}
	return s01CDN + ws + ".zip"
}

// S01Mod is one catalogue entry.
type S01Mod struct {
	ID          string   `json:"_id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Category    string   `json:"category"` // "Free" or "Early Access"
	Cover       string   `json:"cover"`
	Slug        string   `json:"slug"`
	Version     string   `json:"currentVersion"`
	Views       int      `json:"viewsCount"`
	Likes       int      `json:"likesCount"`
	Versions    int      `json:"versionsCount"`
	Created     string   `json:"createdAt"`
	Steam       int64    `json:"steam"`
	Tags        []string `json:"tags"`     // the current version's, e.g. "Early Access"
	MinLevel    int      `json:"minLevel"` // subscription tier the files need (0: none)
}

// Free reports whether the mod is free: its catalogue category is "Free".
func (m S01Mod) Free() bool { return m.Category == "Free" }

// SiteFileFree reports whether the newest file on 01studio.dev itself is
// free: a free mod whose current version isn't Early Access or for a
// subscription tier.
func (m S01Mod) SiteFileFree() bool {
	if !m.Free() || m.Version == "" || m.MinLevel > 0 {
		return false
	}
	for _, t := range m.Tags {
		if strings.EqualFold(t, "Early Access") {
			return false
		}
	}
	return true
}

// Page is the mod's page on 01studio.dev.
func (m S01Mod) Page() string { return s01Site + "/mods/" + m.Slug }

// Image is the cover image.
func (m S01Mod) Image() string {
	if m.Cover == "" {
		return ""
	}
	return s01API + "/v2/files/" + m.Cover
}

// WorkshopID is the Steam Workshop item the mod was published as.
func (m S01Mod) WorkshopID() string {
	if m.Steam <= 0 {
		return ""
	}
	return fmt.Sprint(m.Steam)
}

// CreatedTime is when the entry was published on 01studio.dev.
func (m S01Mod) CreatedTime() time.Time {
	t, _ := time.Parse(time.RFC3339, m.Created)
	return t
}

var (
	s01Mu  sync.Mutex
	s01All []S01Mod
	s01At  time.Time
)

// S01All returns the whole catalogue (about 120 mods), cached for an hour.
func S01All() ([]S01Mod, error) {
	s01Mu.Lock()
	defer s01Mu.Unlock()
	if s01All != nil && time.Since(s01At) < time.Hour {
		return s01All, nil
	}
	var all []S01Mod
	done := false
	for page := 1; page <= 50 && !done; page++ {
		var out struct {
			Status struct {
				Code  int    `json:"code"`
				Error string `json:"error"`
			} `json:"status"`
			Pager struct {
				TotalPages int `json:"totalPages"`
			} `json:"pager"`
			Result []S01Mod `json:"result"`
		}
		if err := s01Post("/v2/mods", map[string]any{"page": page, "category": ""}, &out); err != nil {
			if all != nil {
				done = true // a partial listing is not a cap hit
				break
			}
			return nil, err
		}
		if out.Status.Code != 200 {
			return nil, fmt.Errorf("01studio.dev: %d %s", out.Status.Code, out.Status.Error)
		}
		all = append(all, out.Result...)
		if page >= out.Pager.TotalPages || len(out.Result) == 0 {
			done = true
		}
	}
	if !done {
		Warn("01studio.dev lists more than %d mods; the rest is not shown", len(all))
	}
	s01All, s01At = all, time.Now()
	return all, nil
}

// S01ByWorkshopID finds the 01 STUDIO entry for a Workshop item.
func S01ByWorkshopID(ws string) (*S01Mod, error) {
	all, err := S01All()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].WorkshopID() == ws {
			return &all[i], nil
		}
	}
	return nil, nil
}

func s01Post(path string, body any, v any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", s01API+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	setHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("01studio.dev: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(v)
}

// S01Matches reports whether every word of q is in the title.
func S01Matches(m S01Mod, q string) bool {
	t := strings.ToLower(m.Title + " " + m.Slug)
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(t, w) {
			return false
		}
	}
	return true
}

// SetS01ForTest replaces the catalogue (tests); the returned func restores it.
func SetS01ForTest(list []S01Mod) func() {
	s01Mu.Lock()
	old, oldAt := s01All, s01At
	s01All, s01At = list, time.Now().Add(100*365*24*time.Hour)
	s01Mu.Unlock()
	return func() {
		s01Mu.Lock()
		s01All, s01At = old, oldAt
		s01Mu.Unlock()
	}
}
