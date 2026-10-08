// Package cfclear passes Cloudflare's browser check the honest way: it opens
// the page in a real browser on this PC (the person's own Edge or Chrome,
// with a profile of ppgmods' own - never their personal profile), lets the
// browser run the check exactly as it would when the person visits the page,
// and captures the clearance cookie the check ends with. If the check wants
// a click, the window is on screen so the person can click it - the same
// thing they would do in their own browser.
//
// The clearance is bound to the IP it was earned on and to the browser's
// User-Agent, and this runs on the same PC as the person's browser, so a
// clearance their browser legitimately earned is valid for ppgmods too.
package cfclear

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/Trlydev/SWG/internal/manager"
)

// SkymodsSite is the clearance site of the Skymods catalogue.
const SkymodsSite = "smods.ru"

// Clearance is a stored check result for one site: the cf_clearance cookie
// and the User-Agent it was issued to. Both are needed: Cloudflare binds the
// cookie to the browser that earned it.
type Clearance struct {
	Site      string    `json:"site"`
	Cookie    string    `json:"cookie"`
	UserAgent string    `json:"user_agent"`
	At        time.Time `json:"at"`
}

func path(site string) (string, error) {
	d, err := manager.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "clearance-"+site+".json"), nil
}

// Load returns the stored clearance for a site, or nil if none is stored.
func Load(site string) (*Clearance, error) {
	p, err := path(site)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Clearance
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save stores a clearance (only readable by the user).
func Save(c *Clearance) error {
	p, err := path(c.Site)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// Clear forgets a stored clearance (it is fine if there is none).
func Clear(site string) error {
	p, err := path(site)
	if err != nil {
		return err
	}
	if err := os.Remove(p); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return nil
}
