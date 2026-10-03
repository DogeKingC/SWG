package sources

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

const (
	gbAPI    = "https://gamebanana.com/apiv11"
	GBGameID = 7715 // People Playground
)

type GBMod struct {
	ID        int    `json:"_idRow"`
	Name      string `json:"_sName"`
	URL       string `json:"_sProfileUrl"`
	Modified  int64  `json:"_tsDateModified"`
	Submitter struct {
		Name string `json:"_sName"`
	} `json:"_aSubmitter"`
	Category struct {
		Name string `json:"_sName"`
	} `json:"_aRootCategory"`
	Game struct {
		ID int `json:"_idRow"`
	} `json:"_aGame"`
	Preview struct {
		Images []struct {
			Base string `json:"_sBaseUrl"`
			File string `json:"_sFile"`
			F220 string `json:"_sFile220"`
		} `json:"_aImages"`
	} `json:"_aPreviewMedia"`
}

// Thumb returns a small preview image URL, or "".
func (m GBMod) Thumb() string {
	for _, im := range m.Preview.Images {
		f := im.F220
		if f == "" {
			f = im.File
		}
		if im.Base != "" && f != "" {
			return im.Base + "/" + f
		}
	}
	return ""
}

type GBFile struct {
	ID          int    `json:"_idRow"`
	Name        string `json:"_sFile"`
	Size        int64  `json:"_nFilesize"`
	Added       int64  `json:"_tsDateAdded"`
	DownloadURL string `json:"_sDownloadUrl"`
	MD5         string `json:"_sMd5Checksum"`
	Analysis    string `json:"_sAnalysisResult"`
	AVState     string `json:"_sAvState"`
	AVResult    string `json:"_sAvResult"`
	Version     string `json:"_sVersion"`
}

func (f GBFile) AddedTime() time.Time { return time.Unix(f.Added, 0) }

// Clean reports whether GameBanana's own malware analysis finished and passed.
func (f GBFile) Clean() bool {
	return f.AVState == "done" && f.AVResult == "clean" && f.Analysis == "ok"
}

func getJSON(u string, v any) error {
	resp, err := get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// GBSearch searches People Playground mods by name. An empty query lists the
// newest mods.
func GBSearch(query string, page int) ([]GBMod, error) {
	var out struct {
		Records []GBMod `json:"_aRecords"`
	}
	var u string
	if query == "" {
		u = fmt.Sprintf("%s/Mod/Index?_nPage=%d&_nPerpage=20&_aFilters%%5BGeneric_Game%%5D=%d", gbAPI, page, GBGameID)
	} else {
		u = fmt.Sprintf("%s/Util/Search/Results?_sModelName=Mod&_sOrder=best_match&_idGameRow=%d&_sSearchString=%s&_nPage=%d", gbAPI, GBGameID, url.QueryEscape(query), page)
	}
	if err := getJSON(u, &out); err != nil {
		return nil, err
	}
	var mods []GBMod
	for _, m := range out.Records {
		if m.Game.ID == GBGameID {
			mods = append(mods, m)
		}
	}
	return mods, nil
}

func GBGetMod(id int) (*GBMod, error) {
	var m GBMod
	u := fmt.Sprintf("%s/Mod/%d?_csvProperties=_idRow,_sName,_sProfileUrl,_tsDateModified,_aSubmitter,_aRootCategory,_aGame", gbAPI, id)
	if err := getJSON(u, &m); err != nil {
		return nil, err
	}
	if m.Game.ID != GBGameID {
		return nil, fmt.Errorf("gamebanana mod %d is not a People Playground mod", id)
	}
	return &m, nil
}

// GBFiles returns the downloadable files of a mod, newest first.
func GBFiles(id int) ([]GBFile, error) {
	var out struct {
		Files []GBFile `json:"_aFiles"`
	}
	if err := getJSON(fmt.Sprintf("%s/Mod/%d?_csvProperties=_aFiles", gbAPI, id), &out); err != nil {
		return nil, err
	}
	fs := out.Files
	for i := 1; i < len(fs); i++ {
		for j := i; j > 0 && fs[j].Added > fs[j-1].Added; j-- {
			fs[j], fs[j-1] = fs[j-1], fs[j]
		}
	}
	return fs, nil
}
