// Package manager installs, updates, verifies and rolls back mods, enforcing
// the safety policy around every change to the Mods folder.
package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Installed struct {
	Key          string            `json:"key"` // gb:<modid>, sky:<workshopid>, local:<sha256 prefix>
	Name         string            `json:"name"`
	Author       string            `json:"author,omitempty"`
	Source       string            `json:"source"`
	Folders      []string          `json:"folders"` // folder names under Mods/
	FileID       int               `json:"file_id,omitempty"`
	Version      string            `json:"version,omitempty"`
	Mirror       string            `json:"mirror,omitempty"`        // Workshop items: mirror copy installed
	Kind         string            `json:"kind,omitempty"`          // "mod" (default) or "contraption"
	Adopted      bool              `json:"adopted,omitempty"`       // found already installed, not installed by ppgmods
	ScanMax      string            `json:"scan_max,omitempty"`      // highest scanner finding at install/adoption
	RiskAccepted bool              `json:"risk_accepted,omitempty"` // installed despite CRITICAL findings, by the person's choice
	Aliases      []string          `json:"aliases,omitempty"`       // other refs for the same mod, e.g. tw:30 for sky:2516131949
	Revision     time.Time         `json:"revision,omitempty"`      // upload/revision time at the source
	ArchiveSHA   string            `json:"archive_sha256,omitempty"`
	Files        map[string]string `json:"files"` // "<folder>/<rel>" -> sha256
	Findings     []string          `json:"findings,omitempty"`
	InstalledAt  time.Time         `json:"installed_at"`
	Pinned       bool              `json:"pinned,omitempty"`
	// Quarantined says why the item's folders were moved out of the game
	// folder (into the quarantine area), so the game can't load it; "" when
	// it is in place.
	Quarantined   string     `json:"quarantined,omitempty"`
	QuarantinedAt *time.Time `json:"quarantined_at,omitempty"`
	// Off: the mod is turned off; its folders are in ppgmods' "off" area
	// (see TurnOff), not the game folder.
	Off bool `json:"off,omitempty"`
}

type State struct {
	path string
	Mods map[string]*Installed `json:"mods"`
	// Profiles are saved lists of the mods that are on (keys); Profile is
	// the one last saved or used.
	Profiles map[string][]string `json:"profiles,omitempty"`
	Profile  string              `json:"profile,omitempty"`
}

func ConfigDir() (string, error) {
	if d := os.Getenv("PPGMODS_HOME"); d != "" {
		return d, os.MkdirAll(d, 0o755)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, "ppgmods")
	return d, os.MkdirAll(d, 0o755)
}

func LoadState() (*State, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	s := &State{path: filepath.Join(dir, "state.json"), Mods: map[string]*Installed{}}
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Mods == nil {
		s.Mods = map[string]*Installed{}
	}
	return s, nil
}

func (s *State) Save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *State) Sorted() []*Installed {
	var out []*Installed
	for _, m := range s.Mods {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Find returns the entry installed under key or under one of its aliases.
func (s *State) Find(key string) *Installed {
	if m := s.Mods[key]; m != nil {
		return m
	}
	for _, m := range s.Mods {
		for _, a := range m.Aliases {
			if a == key {
				return m
			}
		}
	}
	return nil
}

// OwnerOf returns the entry that installed a Mods/ or Contraptions/ folder.
func (s *State) OwnerOf(kind, folder string) *Installed {
	for _, m := range s.Mods {
		k := m.Kind
		if k == "" {
			k = "mod"
		}
		if k != kind {
			continue
		}
		for _, f := range m.Folders {
			if f == folder {
				return m
			}
		}
	}
	return nil
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashTree hashes every file under dir, keyed "<prefix>/<rel path>".
func hashTree(dir, prefix string, into map[string]string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		h, err := fileSHA(p)
		if err != nil {
			return err
		}
		// state.json stores names as UTF-8 text: a name that isn't valid
		// UTF-8 (possible on disk) is saved with U+FFFD, so record it the
		// same way or it never matches again.
		into[strings.ToValidUTF8(prefix+"/"+filepath.ToSlash(rel), "\uFFFD")] = h
		return nil
	})
}

// FileSHA returns the hex SHA-256 of a file.
func FileSHA(p string) (string, error) { return fileSHA(p) }

// TreeSHA returns a stable hex SHA-256 over every file path and hash in dir.
func TreeSHA(dir string) (string, error) {
	files := map[string]string{}
	if err := hashTree(dir, ".", files); err != nil {
		return "", err
	}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		io.WriteString(h, k+"\x00"+files[k]+"\n")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
