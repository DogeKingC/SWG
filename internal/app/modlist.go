package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Trlydev/SWG/internal/manager"
)

// A mod list is a shareable file naming the installed mods by where they
// come from (gb:, sky:, tw:, nx:, ow:). Importing one installs each through
// the same download, scan and safety checks as any install: the file only
// says what to fetch, never what to trust.

// ModListFormat identifies a mod list file.
const ModListFormat = "ppgmods-mod-list"

// ModList is a mod list file.
type ModList struct {
	Format   string        `json:"format"`
	Version  int           `json:"version"`
	Exported time.Time     `json:"exported"`
	Profile  string        `json:"profile,omitempty"`
	Mods     []ModListItem `json:"mods"`
}

// ModListItem is one mod of a list.
type ModListItem struct {
	Ref     string `json:"ref"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind,omitempty"` // mod or contraption
	Off     bool   `json:"off,omitempty"`  // installed but turned off
}

// maxListItems caps an imported list.
const maxListItems = 1000

var reListRef = regexp.MustCompile(`^(gb|tw|sky|nx|ow):[0-9a-z][0-9a-z-]{0,63}$`)

// ExportList lists the installed items that can be downloaded again;
// skipped counts the ones that can't (found on this PC without a source).
func ExportList(st *manager.State) (l *ModList, skipped int) {
	l = &ModList{Format: ModListFormat, Version: 1, Exported: time.Now().UTC(), Profile: st.Profile}
	for _, inst := range st.Sorted() {
		if !reListRef.MatchString(inst.Key) {
			skipped++
			continue
		}
		kind := inst.Kind
		if kind == "" {
			kind = manager.KindMod
		}
		l.Mods = append(l.Mods, ModListItem{Ref: inst.Key, Name: inst.Name, Version: inst.Version, Kind: kind, Off: inst.Off})
	}
	return l, skipped
}

// ParseList reads a mod list file, refusing anything that isn't one.
func ParseList(b []byte) (*ModList, error) {
	if len(b) > 1<<20 {
		return nil, errors.New("that file is too large to be a mod list")
	}
	var l ModList
	if err := json.Unmarshal(b, &l); err != nil || l.Format != ModListFormat {
		return nil, errors.New("that file isn't a ppgmods mod list")
	}
	if len(l.Mods) > maxListItems {
		return nil, fmt.Errorf("the list has %d items; at most %d can be imported at once", len(l.Mods), maxListItems)
	}
	seen := map[string]bool{}
	var mods []ModListItem
	for _, it := range l.Mods {
		it.Ref = strings.TrimSpace(it.Ref)
		if !reListRef.MatchString(it.Ref) {
			return nil, fmt.Errorf("the list names %q, which isn't a mod ppgmods can download", it.Ref)
		}
		if !seen[it.Ref] {
			seen[it.Ref] = true
			mods = append(mods, it)
		}
	}
	l.Mods = mods
	return &l, nil
}

// ImportList installs the list's items that aren't installed yet, then
// turns off those of them the list has off. Mods already installed are left
// as they are.
func (a *App) ImportList(m *manager.Manager, l *ModList) Summary {
	var refs []string
	var s Summary
	added := map[string]bool{}
	for _, it := range l.Mods {
		if m.State.Find(it.Ref) != nil {
			s.Current++
			continue
		}
		refs = append(refs, it.Ref)
		added[it.Ref] = true
	}
	a.logf("mod list: %d item(s), %d already installed, installing %d", len(l.Mods), s.Current, len(refs))
	got := a.InstallMany(m, refs)
	got.Current = s.Current
	for _, it := range l.Mods {
		if inst := m.State.Find(it.Ref); it.Off && added[it.Ref] && inst != nil && !inst.Off {
			if err := m.TurnOff(inst.Key); err != nil {
				a.logf("%s: %v", it.Ref, err)
			}
		}
	}
	a.logf("mod list imported: %d installed, %d already there, %d refused, %d errors", got.OK, got.Current, got.Refused, got.Failed)
	return got
}
