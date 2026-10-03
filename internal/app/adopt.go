package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/scan"
	"github.com/DogeKingC/SWG/internal/sources"
)

// Found is a mod or contraption that was already in the game folders.
type Found struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Folder  string `json:"folder"`
	Version string `json:"version,omitempty"`
	How     string `json:"how"` // how it was identified
	ScanMax string `json:"scan_max"`
}

var reLocalKey = regexp.MustCompile(`[^a-z0-9]+`)

// FindExisting starts tracking mods and contraptions the player installed
// without ppgmods, identifying each when possible: a mod.json
// CreatorUGCIdentity is its Steam Workshop ID; otherwise a matching True
// Workshop upload (same name and author); otherwise it is kept as
// "local:<folder>" so it at least shows as installed and is verified.
func (a *App) FindExisting(m *manager.Manager) ([]Found, error) {
	var out []Found
	tw, _ := twCatalogue() // best effort: works offline too, just with fewer matches
	for _, kind := range []string{manager.KindMod, manager.KindContraption} {
		base := m.ModsDir
		if kind == manager.KindContraption {
			base = m.ContraptionsDir
		}
		ents, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || m.State.OwnerOf(kind, e.Name()) != nil {
				continue
			}
			dir := filepath.Join(base, e.Name())
			f := Found{Kind: kind, Folder: e.Name(), Name: e.Name()}
			var author, ugc string
			if kind == manager.KindMod {
				mj, ok := readModJSON(dir)
				if !ok {
					continue // not a mod folder
				}
				if mj.Name != "" {
					f.Name = mj.Name
				}
				author, f.Version, ugc = mj.Author, mj.ModVersion, mj.UGC()
			} else {
				name, ok := contraptionName(dir)
				if !ok {
					continue
				}
				f.Name = name
			}
			source := dir
			twKey, twHow, twPage := "", "", ""
			for _, it := range tw {
				if it.Type == kind && sameMod(it.Title, it.Author, f.Name, author) {
					twKey, twHow, twPage = fmt.Sprintf("tw:%d", it.ID), "matches True Workshop upload \""+it.Title+"\"", it.Page()
					break
				}
			}
			var aliases []string
			switch {
			case ugc != "":
				f.Key, f.How = "sky:"+ugc, "Steam Workshop ID in mod.json"
				source = "https://steamcommunity.com/sharedfiles/filedetails/?id=" + ugc
				if twKey != "" {
					aliases = append(aliases, twKey) // also shows as installed on its True Workshop card
					f.How += "; " + twHow
				}
			case twKey != "":
				f.Key, f.How, source = twKey, twHow, twPage
			}
			if f.Key == "" || m.State.Find(f.Key) != nil {
				if f.Key != "" {
					f.How += " (already installed elsewhere; tracked as a local copy)"
				} else {
					f.How = "not found on any source; tracked as a local copy"
				}
				f.Key = "local:" + kind[:1] + "-" + strings.Trim(reLocalKey.ReplaceAllString(strings.ToLower(e.Name()), "-"), "-")
			}
			rep, err := m.Adopt(f.Key, f.Name, source, f.Version, kind, e.Name(), aliases...)
			if err != nil {
				a.logf("  %s: %v", e.Name(), err)
				continue
			}
			f.ScanMax = "none"
			if rep.Max() >= 0 {
				f.ScanMax = rep.Max().String()
			}
			warn := ""
			if rep.Max() >= scan.High {
				warn = "  <-- scanner: " + f.ScanMax + ", review this mod"
			}
			a.logf("found %s %q in %s -> %s (%s)%s", kind, f.Name, e.Name(), f.Key, f.How, warn)
			out = append(out, f)
		}
	}
	return out, nil
}

type modJSONInfo struct {
	Name               string `json:"Name"`
	Author             string `json:"Author"`
	ModVersion         string `json:"ModVersion"`
	CreatorUGCIdentity any    `json:"CreatorUGCIdentity"`
}

func (mj modJSONInfo) UGC() string {
	if mj.CreatorUGCIdentity == nil {
		return ""
	}
	u := strings.TrimSpace(fmt.Sprint(mj.CreatorUGCIdentity))
	if !reWorkshopDir.MatchString(u) {
		return ""
	}
	return u
}

// readModJSON reads mod.json from dir or one level below it.
func readModJSON(dir string) (modJSONInfo, bool) {
	var mj modJSONInfo
	cands := []string{filepath.Join(dir, "mod.json")}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				cands = append(cands, filepath.Join(dir, e.Name(), "mod.json"))
			}
		}
	}
	for _, c := range cands {
		if b, err := os.ReadFile(c); err == nil {
			if json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &mj) == nil {
				return mj, true
			}
		}
	}
	return mj, false
}

// contraptionName returns the name of the contraption saved in dir.
func contraptionName(dir string) (string, bool) {
	name := ""
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && name == "" && !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".jaap") {
			name = strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
			var meta struct{ DisplayName, Name string }
			if b, err := os.ReadFile(strings.TrimSuffix(p, filepath.Ext(p)) + ".json"); err == nil && json.Unmarshal(b, &meta) == nil {
				if meta.DisplayName != "" {
					name = meta.DisplayName
				} else if meta.Name != "" {
					name = meta.Name
				}
			}
		}
		return nil
	})
	return name, name != ""
}

// twCatalogue is the True Workshop catalogue (replaced in tests).
var twCatalogue = sources.TWAll
