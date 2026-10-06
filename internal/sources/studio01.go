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
// on its own site. The catalogue is public; files are given to signed-in
// users only (some to supporters only), so ppgmods lists the mods, links
// each to its Steam Workshop ID (every entry has one, so the Workshop
// mirrors can provide a copy), and for the site's own newest version opens
// the page in the browser, where the person is signed in.

const (
	s01API  = "https://api.01studio.dev"
	s01Site = "https://01studio.dev"
)

// S01Mod is one catalogue entry.
type S01Mod struct {
	ID          string `json:"_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"` // "Free" or "Early Access"
	Cover       string `json:"cover"`
	Slug        string `json:"slug"`
	Version     string `json:"currentVersion"`
	Views       int    `json:"viewsCount"`
	Likes       int    `json:"likesCount"`
	Versions    int    `json:"versionsCount"`
	Created     string `json:"createdAt"`
	Steam       int64  `json:"steam"`
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
