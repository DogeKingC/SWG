package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Trlydev/SWG/internal/manager"
	"github.com/Trlydev/SWG/internal/workshop"
)

// While the Open Workshop is paused nothing installs or updates from it,
// versions from the suspect window are hidden, and Verify names installed
// copies from that window.
func TestOpenWorkshopPause(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-72 * time.Hour)
	ix := &workshop.Index{Paused: true, PausedReason: "worm reported", PausedAt: &now, SuspectSince: &since, Entries: []workshop.Entry{
		{Slug: "old-mod", Name: "Old", Kind: "mod", Published: now.Add(-30 * 24 * time.Hour), SHA256: strings.Repeat("a", 64)},
		{Slug: "new-mod", Name: "New", Kind: "mod", Published: now.Add(-time.Hour), SHA256: strings.Repeat("b", 64)},
	}}
	defer workshop.SetIndexForTest(ix)()

	a := &App{Logf: func(string, ...any) {}}
	for _, slug := range []string{"old-mod", "new-mod"} {
		var rej *manager.Rejection
		if _, err := a.fetchOW(slug, nil); !errors.As(err, &rej) {
			t.Errorf("%s: install not refused while paused: %v", slug, err)
		}
	}
	if c, err := a.owUpdate(&manager.Installed{Key: "ow:old-mod", ArchiveSHA: "x"}); c != nil || err != nil {
		t.Errorf("update offered while paused: %v %v", c, err)
	}

	var r SearchResults
	searchOW(&r, "", 1, SearchOpts{Kind: "mod"})
	if len(r.Errors) == 0 || !strings.Contains(r.Errors[0], "worm reported") {
		t.Errorf("search did not say the Open Workshop is paused: %v", r.Errors)
	}
	for _, e := range r.OpenWS {
		if e.Ref == "ow:new-mod" {
			t.Error("a version from the suspect window is still listed")
		}
	}

	if why := owWithdrawn(&manager.Installed{Key: "ow:new-mod", Revision: now.Add(-time.Hour)}); why == "" {
		t.Error("Verify does not flag an installed version from the suspect window")
	}
	if why := owWithdrawn(&manager.Installed{Key: "ow:old-mod", Revision: now.Add(-30 * 24 * time.Hour)}); why != "" {
		t.Errorf("Verify flags a version from before the window: %s", why)
	}

	// Resumed: installs work again (the old one; withdrawn ones stay out).
	ix.Paused, ix.SuspectSince = false, nil
	if why := ix.Blocked(&ix.Entries[1]); why != "" {
		t.Errorf("still held back after resume: %s", why)
	}
}
