package sources

import (
	"encoding/json"
	"testing"
)

// Entries as api.01studio.dev lists them (October 2026): a paid Early
// Access mod with no Workshop copy, a free mod whose site files are Early
// Access, and a free mod with no site files at all.
func TestS01Free(t *testing.T) {
	raw := `[
	 {"slug":"souls-playground","category":"Early Access","tags":["Early Access"],"minLevel":null,"currentVersion":"3.5","steam":-1},
	 {"slug":"jujutsu-playground-mod","category":"Free","tags":["Early Access"],"minLevel":null,"currentVersion":"10.0","steam":3576213306},
	 {"slug":"chainsaw-playground","category":"Free","tags":[],"minLevel":null,"currentVersion":null,"steam":3730363397},
	 {"slug":"free-site-file","category":"Free","tags":[],"minLevel":null,"currentVersion":"1.0","steam":1},
	 {"slug":"tier-site-file","category":"Free","tags":[],"minLevel":2,"currentVersion":"1.0","steam":1}
	]`
	var ms []S01Mod
	if err := json.Unmarshal([]byte(raw), &ms); err != nil {
		t.Fatal(err)
	}
	want := []struct{ free, site bool }{{false, false}, {true, false}, {true, false}, {true, true}, {true, false}}
	for i, m := range ms {
		if m.Free() != want[i].free || m.SiteFileFree() != want[i].site {
			t.Errorf("%s: Free %v, SiteFileFree %v; want %v, %v", m.Slug, m.Free(), m.SiteFileFree(), want[i].free, want[i].site)
		}
	}
}
