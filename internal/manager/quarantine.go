package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Quarantine: a suspicious item's folders are moved out of Mods/ (or
// Contraptions/) into ppgmods' quarantine area, where the game can't load
// them. Nothing is deleted; Release puts them back exactly as they were.

func (m *Manager) quarantineDir(inst *Installed) (string, error) {
	base, err := m.workDir("quarantine")
	if err != nil {
		return "", err
	}
	return filepath.Join(base, strings.ReplaceAll(inst.Key, ":", "-")), nil
}

// Quarantine moves an installed item out of the game folder.
func (m *Manager) Quarantine(key, reason string) error {
	inst := m.State.Mods[key]
	if inst == nil {
		return fmt.Errorf("%s is not installed", key)
	}
	if inst.Quarantined != "" {
		return fmt.Errorf("%s is already in quarantine", key)
	}
	if inst.Off {
		return fmt.Errorf("%s is turned off (the game doesn't load it); remove it, or turn it on first", key)
	}
	q, err := m.quarantineDir(inst)
	if err != nil {
		return err
	}
	if _, err := os.Stat(q); err == nil {
		return fmt.Errorf("%s already exists; move it away first", q)
	}
	base := m.dirFor(inst)
	var moved []string
	for _, f := range inst.Folders {
		if err := plainFolder(base, f); err != nil {
			return err
		}
		src := filepath.Join(base, f)
		if _, err := os.Stat(src); err != nil {
			continue // already gone
		}
		if err := moveTree(src, filepath.Join(q, f)); err != nil {
			for _, back := range moved { // put back what was moved
				moveTree(filepath.Join(q, back), filepath.Join(base, back))
			}
			os.RemoveAll(q)
			return fmt.Errorf("quarantining %s: %w (is the game running?)", f, err)
		}
		moved = append(moved, f)
	}
	now := time.Now().UTC()
	inst.Quarantined, inst.QuarantinedAt = strings.TrimSpace(reason), &now
	if inst.Quarantined == "" {
		inst.Quarantined = "quarantined by you"
	}
	m.logf("quarantined %s (%s): the game can no longer load it; `release %s` puts it back", key, strings.Join(inst.Folders, ", "), key)
	return m.State.Save()
}

// Release puts a quarantined item back into the game folder.
func (m *Manager) Release(key string) error {
	inst := m.State.Mods[key]
	if inst == nil {
		return fmt.Errorf("%s is not installed", key)
	}
	if inst.Quarantined == "" {
		return fmt.Errorf("%s is not in quarantine", key)
	}
	q, err := m.quarantineDir(inst)
	if err != nil {
		return err
	}
	base := m.dirFor(inst)
	for _, f := range inst.Folders {
		if err := plainFolder(base, f); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(base, f)); err == nil {
			return fmt.Errorf("%s is in the game folder again (put there by something else); remove it before releasing the quarantined copy", filepath.Join(base, f))
		}
	}
	for _, f := range inst.Folders {
		src := filepath.Join(q, f)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := moveTree(src, filepath.Join(base, f)); err != nil {
			return err
		}
	}
	os.RemoveAll(q)
	inst.Quarantined, inst.QuarantinedAt = "", nil
	m.logf("released %s from quarantine; Verify checks it against what was installed", key)
	return m.State.Save()
}

// deleteQuarantined deletes a quarantined item's folders for good.
func (m *Manager) deleteQuarantined(inst *Installed) error {
	q, err := m.quarantineDir(inst)
	if err != nil {
		return err
	}
	return os.RemoveAll(q)
}

// plainFolder checks a folder name from state.json: only a plain name
// directly inside base.
func plainFolder(base, name string) error {
	if base == "" || name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return fmt.Errorf("refusing to move %q: not a folder inside %s", name, base)
	}
	return nil
}

// errQuarantined refuses changes that would bring a quarantined item back
// without a deliberate Release.
func errQuarantined(inst *Installed) error {
	return &Rejection{[]string{fmt.Sprintf("%s is in quarantine (%s): release or remove it first", inst.Key, inst.Quarantined)}}
}
