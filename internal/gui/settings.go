package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/Trlydev/SWG/internal/app"
	"github.com/Trlydev/SWG/internal/manager"
)

var (
	themeMode      = "dark" // "dark", "light" or "system" (see themeSetting)
	bgThumbsOff    bool
	skymodsCheckOn bool = true // offer/run the Skymods browser check when Cloudflare asks
)

// settings are the GUI preferences saved between runs. Safety overrides are
// deliberately not persisted: they apply to one install at a time.
type settings struct {
	Game           string  `json:"game"`
	CooldownHours  float64 `json:"cooldown_hours"`
	Offline        bool    `json:"offline"`
	NoBgThumbs     bool    `json:"no_bg_thumbs"`     // don't fetch missing Workshop thumbnails in the background
	NoSkymodsCheck bool    `json:"no_skymods_check"` // never run the Skymods browser check
	Theme          string  `json:"theme,omitempty"`  // "dark" (default), "light" or "system"
}

// themeSetting normalises a saved theme: dark unless light or system.
func themeSetting(s string) string {
	if s == "light" || s == "system" {
		return s
	}
	return "dark"
}

func settingsPath() (string, error) {
	d, err := manager.ConfigDir()
	return filepath.Join(d, "gui-settings.json"), err
}

func loadSettings() (*settings, error) {
	p, err := settingsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var st settings
	return &st, json.Unmarshal(b, &st)
}

func (st *settings) save() error {
	p, err := settingsPath()
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(p, b, 0o644)
}

func (st *settings) apply(o *app.Options) {
	bgThumbsOff = st.NoBgThumbs
	skymodsCheckOn = !st.NoSkymodsCheck
	themeMode = themeSetting(st.Theme)
	o.Game = st.Game
	o.Offline = st.Offline
	if st.CooldownHours >= 0 {
		o.Policy.Cooldown = time.Duration(st.CooldownHours * float64(time.Hour))
	}
}

func settingsFrom(o app.Options) settings {
	return settings{Game: o.Game, CooldownHours: o.Policy.Cooldown.Hours(), Offline: o.Offline,
		NoBgThumbs: bgThumbsOff, NoSkymodsCheck: !skymodsCheckOn, Theme: themeMode}
}
