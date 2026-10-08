package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Turning mods off, and profiles. A mod that is off has its folders moved
// out of Mods/ into ppgmods' "off" area, where the game can't load them;
// turning it on moves them back. Nothing is deleted. A profile is a saved
// list of the mods that are on; using it turns mods on and off to match.
// Quarantined mods and contraptions are left alone.

func (m *Manager) offDir(inst *Installed) (string, error) {
	base, err := m.workDir("off")
	if err != nil {
		return "", err
	}
	return filepath.Join(base, strings.ReplaceAll(inst.Key, ":", "-")), nil
}

// moveFolders moves inst's folders from one base folder to another; on a
// failure it puts back what it moved.
func moveFolders(inst *Installed, from, to string) error {
	for _, f := range inst.Folders {
		if err := plainFolder(from, f); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(to, f)); err == nil {
			return fmt.Errorf("%s already exists; move it away first", filepath.Join(to, f))
		}
	}
	var moved []string
	for _, f := range inst.Folders {
		src := filepath.Join(from, f)
		if _, err := os.Stat(src); err != nil {
			continue // already gone
		}
		if err := moveTree(src, filepath.Join(to, f)); err != nil {
			for _, back := range moved {
				moveTree(filepath.Join(to, back), filepath.Join(from, back))
			}
			return fmt.Errorf("moving %s: %w (is the game running?)", f, err)
		}
		moved = append(moved, f)
	}
	return nil
}

// TurnOff moves an installed mod out of the game folder, so the game
// doesn't load it, until TurnOn.
func (m *Manager) TurnOff(key string) error {
	if err := m.turnOff(key); err != nil {
		return err
	}
	return m.State.Save()
}

func (m *Manager) turnOff(key string) error {
	inst := m.State.Mods[key]
	switch {
	case inst == nil:
		return fmt.Errorf("%s is not installed", key)
	case inst.Off:
		return nil
	case inst.Quarantined != "":
		return fmt.Errorf("%s is in quarantine", key)
	case inst.Kind == KindContraption:
		return fmt.Errorf("%s is a contraption; only mods can be turned off", key)
	}
	dest, err := m.offDir(inst)
	if err != nil {
		return err
	}
	if err := moveFolders(inst, m.dirFor(inst), dest); err != nil {
		os.Remove(dest)
		return fmt.Errorf("turning off %s: %w", key, err)
	}
	inst.Off = true
	m.logf("turned off %s: the game won't load it until you turn it on", key)
	return nil
}

// TurnOn puts a mod that was turned off back into the game folder.
func (m *Manager) TurnOn(key string) error {
	if err := m.turnOn(key); err != nil {
		return err
	}
	return m.State.Save()
}

func (m *Manager) turnOn(key string) error {
	inst := m.State.Mods[key]
	if inst == nil {
		return fmt.Errorf("%s is not installed", key)
	}
	if !inst.Off {
		return nil
	}
	src, err := m.offDir(inst)
	if err != nil {
		return err
	}
	if err := moveFolders(inst, src, m.dirFor(inst)); err != nil {
		return fmt.Errorf("turning on %s: %w", key, err)
	}
	os.Remove(src)
	inst.Off = false
	m.logf("turned on %s; Verify checks it against what was installed", key)
	return nil
}

// deleteOff deletes the folders of a mod that is off, for good.
func (m *Manager) deleteOff(inst *Installed) error {
	d, err := m.offDir(inst)
	if err != nil {
		return err
	}
	return os.RemoveAll(d)
}

// errOff refuses changes to a mod that is turned off.
func errOff(inst *Installed) error {
	return &Rejection{[]string{fmt.Sprintf("%s is turned off: turn it on first", inst.Key)}}
}

// Profiles lists the saved profiles' names.
func (s *State) ProfileNames() []string {
	var out []string
	for n := range s.Profiles {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func profileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 || strings.ContainsAny(name, "\x00\r\n") {
		return "", fmt.Errorf("a profile name is 1 to 60 characters on one line")
	}
	return name, nil
}

// SaveProfile saves the mods that are on now as profile name (replacing a
// profile of that name) and makes it the current one.
func (m *Manager) SaveProfile(name string) error {
	name, err := profileName(name)
	if err != nil {
		return err
	}
	var on []string
	for _, inst := range m.State.Sorted() {
		if inst.Kind != KindContraption && !inst.Off && inst.Quarantined == "" {
			on = append(on, inst.Key)
		}
	}
	if m.State.Profiles == nil {
		m.State.Profiles = map[string][]string{}
	}
	m.State.Profiles[name], m.State.Profile = on, name
	m.logf("saved profile %q: %d mod(s) on", name, len(on))
	return m.State.Save()
}

// UseProfile turns mods on and off to match profile name. It returns the
// profile's mods that aren't installed (any more).
func (m *Manager) UseProfile(name string) (missing []string, err error) {
	keys, ok := m.State.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("there is no profile %q", name)
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
		if m.State.Find(k) == nil {
			missing = append(missing, k)
		}
	}
	inProfile := func(inst *Installed) bool {
		if want[inst.Key] {
			return true
		}
		for _, a := range inst.Aliases {
			if want[a] {
				return true
			}
		}
		return false
	}
	var failed []string
	for _, inst := range m.State.Sorted() {
		if inst.Kind == KindContraption || inst.Quarantined != "" {
			continue
		}
		var err error
		if inProfile(inst) {
			err = m.turnOn(inst.Key)
		} else {
			err = m.turnOff(inst.Key)
		}
		if err != nil {
			failed = append(failed, err.Error())
		}
	}
	m.State.Profile = name
	if serr := m.State.Save(); serr != nil {
		return missing, serr
	}
	if len(failed) > 0 {
		return missing, fmt.Errorf("profile %q is only partly in use: %s", name, strings.Join(failed, "; "))
	}
	m.logf("now using profile %q", name)
	return missing, nil
}

// DeleteProfile forgets a saved profile; the mods stay as they are.
func (m *Manager) DeleteProfile(name string) error {
	if _, ok := m.State.Profiles[name]; !ok {
		return fmt.Errorf("there is no profile %q", name)
	}
	delete(m.State.Profiles, name)
	if m.State.Profile == name {
		m.State.Profile = ""
	}
	return m.State.Save()
}
