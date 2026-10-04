package sources

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Nexus Mods' v1 API, used with the person's own API key (Settings → Nexus
// Mods). Premium accounts get download links directly; free accounts get
// them for a file they picked with the site's "Mod Manager Download" button,
// which hands ppgmods an nxm:// link carrying a one-time key.

// NXV1 is the API base (a variable so tests can point it elsewhere).
var NXV1 = "https://api.nexusmods.com/v1"

// NXUser is who an API key belongs to.
type NXUser struct {
	ID      int    `json:"user_id"`
	Name    string `json:"name"`
	Premium bool   `json:"is_premium"`
}

// NXFile is one file of a mod.
type NXFile struct {
	ID       int    `json:"file_id"`
	Name     string `json:"name"`
	FileName string `json:"file_name"`
	Version  string `json:"version"`
	Category string `json:"category_name"`
	Primary  bool   `json:"is_primary"`
	Uploaded int64  `json:"uploaded_timestamp"`
	SizeKB   int64  `json:"size_kb"`
}

// ErrNXKey means Nexus rejected the API key.
var ErrNXKey = errors.New("Nexus Mods rejected the API key (re-link your account in Settings)")

// ErrNXPremium means a direct download needs a premium account.
var ErrNXPremium = errors.New("direct downloads from Nexus Mods need a premium account")

func nxV1(key, path string, v any) error {
	req, err := http.NewRequest("GET", NXV1+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Application-Name", "ppgmods")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
	case 401:
		return ErrNXKey
	case 403:
		return ErrNXPremium
	case 404:
		return fmt.Errorf("Nexus Mods: not found")
	case 429:
		return fmt.Errorf("Nexus Mods: too many requests today, try again later")
	}
	return fmt.Errorf("Nexus Mods: HTTP %d", resp.StatusCode)
}

// NXValidate checks an API key and returns its account.
func NXValidate(key string) (*NXUser, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 1000 || strings.ContainsAny(key, " \t\r\n") {
		return nil, errors.New("that doesn't look like a Nexus Mods API key")
	}
	var u NXUser
	if err := nxV1(key, "/users/validate.json", &u); errors.Is(err, ErrNXKey) {
		return nil, errors.New("Nexus Mods didn't accept that API key: copy the Personal API Key from your account's API page")
	} else if err != nil {
		return nil, err
	}
	return &u, nil
}

// NXFiles lists a mod's files.
func NXFiles(key string, modID int) ([]NXFile, error) {
	var out struct {
		Files []NXFile `json:"files"`
	}
	if err := nxV1(key, fmt.Sprintf("/games/%s/mods/%d/files.json", nxGame, modID), &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

// NXMainFile returns a mod's newest main file (else newest non-archived).
func NXMainFile(key string, modID int) (*NXFile, error) {
	files, err := NXFiles(key, modID)
	if err != nil {
		return nil, err
	}
	rank := func(f NXFile) int {
		switch strings.ToUpper(f.Category) {
		case "MAIN":
			return 3
		case "UPDATE":
			return 2
		case "OPTIONAL", "MISCELLANEOUS":
			return 1
		}
		return -1 // OLD_VERSION, ARCHIVED, DELETED
	}
	sort.SliceStable(files, func(i, j int) bool {
		if rank(files[i]) != rank(files[j]) {
			return rank(files[i]) > rank(files[j])
		}
		return files[i].Uploaded > files[j].Uploaded
	})
	if len(files) == 0 || rank(files[0]) < 0 {
		return nil, fmt.Errorf("Nexus Mods mod %d has no current file", modID)
	}
	return &files[0], nil
}

// NXDownloadURL asks for a download link. nxmKey and expires come from an
// nxm:// link (free accounts); premium accounts leave them empty.
func NXDownloadURL(key string, modID, fileID int, nxmKey, expires string) (string, error) {
	path := fmt.Sprintf("/games/%s/mods/%d/files/%d/download_link.json", nxGame, modID, fileID)
	if nxmKey != "" {
		path += "?" + url.Values{"key": {nxmKey}, "expires": {expires}}.Encode()
	}
	var links []struct {
		Name string `json:"name"`
		URI  string `json:"URI"`
	}
	if err := nxV1(key, path, &links); err != nil {
		return "", err
	}
	for _, l := range links {
		if u, err := url.Parse(l.URI); err == nil && u.Scheme == "https" {
			return l.URI, nil
		}
	}
	return "", errors.New("Nexus Mods returned no download link")
}

// NXMLink is a parsed nxm:// link for People Playground.
type NXMLink struct {
	ModID, FileID int
	Key, Expires  string
}

var (
	reNXMPath = regexp.MustCompile(`^/mods/(\d{1,9})/files/(\d{1,12})$`)
	reNXMTok  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
)

// ErrNXMOtherGame means the link is for another game (another mod manager
// should handle it).
var ErrNXMOtherGame = errors.New("nxm link for another game")

// ParseNXM validates an nxm:// link: only People Playground mod files, with
// a plain key and expiry, nothing else.
func ParseNXM(s string) (*NXMLink, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != "nxm" {
		return nil, errors.New("not an nxm:// link")
	}
	if !strings.EqualFold(u.Host, nxGame) {
		return nil, ErrNXMOtherGame
	}
	m := reNXMPath.FindStringSubmatch(u.Path)
	if m == nil {
		return nil, errors.New("unsupported nxm:// link")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("malformed nxm:// link")
	}
	for k := range q {
		if k != "key" && k != "expires" && k != "user_id" {
			return nil, errors.New("malformed nxm:// link")
		}
	}
	l := &NXMLink{Key: q.Get("key"), Expires: q.Get("expires")}
	l.ModID, _ = strconv.Atoi(m[1])
	l.FileID, _ = strconv.Atoi(m[2])
	if l.Key != "" && !reNXMTok.MatchString(l.Key) || l.Expires != "" && strings.Trim(l.Expires, "0123456789") != "" {
		return nil, errors.New("malformed nxm:// link")
	}
	return l, nil
}
