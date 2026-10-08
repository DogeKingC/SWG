package sources

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Nexus Mods' v3 API (https://api-docs.nexusmods.com/). Its experimental
// "download the repacked archive" endpoint hands out a file link to an API
// key; whether it does so for free accounts is what NXProbe finds out.

// NXV3 is the v3 API base.
const NXV3 = "https://api.nexusmods.com/v3"

// NXV3Error is a v3 API refusal (RFC 9457 problem details).
type NXV3Error struct {
	Status        int
	Title, Detail string
}

func (e *NXV3Error) Error() string {
	return fmt.Sprintf("Nexus Mods: HTTP %d %s: %s", e.Status, e.Title, e.Detail)
}

func nxV3(key, method, path string, v any) error {
	var body io.Reader
	if method == "POST" {
		body = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequest(method, NXV3+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Application-Name", "ppgmods")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		e := &NXV3Error{Status: resp.StatusCode}
		var p struct{ Title, Detail string }
		if json.Unmarshal(b, &p) == nil {
			e.Title, e.Detail = p.Title, p.Detail
		}
		return e
	}
	return json.Unmarshal(b, v)
}

// NXRepackedLink asks the v3 API for a download link of a file (its file
// ID, as in ?file_id= on the site). The link is not followed.
func NXRepackedLink(key string, fileID int) (link string, expires time.Time, err error) {
	var ver struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := nxV3(key, "GET", fmt.Sprintf("/games/%s/mod-file-versions/%d", nxGame, fileID), &ver); err != nil {
		return "", time.Time{}, fmt.Errorf("file %d: %w", fileID, err)
	}
	if ver.Data.ID == "" {
		return "", time.Time{}, fmt.Errorf("file %d: no version id", fileID)
	}
	var out struct {
		URL     string    `json:"download_url"`
		Expires time.Time `json:"expires_at"`
	}
	if err := nxV3(key, "POST", "/mod-file-versions/"+url.PathEscape(ver.Data.ID)+"/download-repacked", &out); err != nil {
		return "", time.Time{}, err
	}
	if u, err := url.Parse(out.URL); err != nil || u.Scheme != "https" {
		return "", time.Time{}, fmt.Errorf("Nexus Mods returned no https link")
	}
	return out.URL, out.Expires, nil
}
