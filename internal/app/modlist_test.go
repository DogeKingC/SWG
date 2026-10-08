package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Trlydev/SWG/internal/manager"
)

// A list holds the items that can be downloaded again, with their on/off
// state; one read back refuses anything that isn't a plain mod reference
// (a path, a URL, another program's link) and drops duplicates.
func TestModList(t *testing.T) {
	st := &manager.State{Mods: map[string]*manager.Installed{
		"gb:1":       {Key: "gb:1", Name: "One", Version: "1.2"},
		"sky:222222": {Key: "sky:222222", Name: "Two", Off: true},
		"local:ab12": {Key: "local:ab12", Name: "Found here"},
		"tw:5":       {Key: "tw:5", Name: "Town", Kind: manager.KindContraption},
	}, Profile: "Main"}
	l, skipped := ExportList(st)
	if skipped != 1 || len(l.Mods) != 3 || l.Profile != "Main" {
		t.Fatalf("export: %d skipped, %+v", skipped, l)
	}
	b, _ := json.Marshal(l)
	back, err := ParseList(b)
	if err != nil || len(back.Mods) != 3 || !back.Mods[1].Off || back.Mods[2].Kind != manager.KindContraption {
		t.Fatalf("read back: %v %+v", err, back)
	}
	for _, bad := range []string{
		`{"format":"something-else","mods":[]}`,
		`not json`,
		`{"format":"ppgmods-mod-list","mods":[{"ref":"../../x"}]}`,
		`{"format":"ppgmods-mod-list","mods":[{"ref":"https://evil.example/x.zip"}]}`,
		`{"format":"ppgmods-mod-list","mods":[{"ref":"local:abc"}]}`,
		`{"format":"ppgmods-mod-list","mods":[{"ref":"gb:1 && calc"}]}`,
		`{"format":"ppgmods-mod-list","mods":[` + strings.Repeat(`{"ref":"gb:1"},`, maxListItems) + `{"ref":"gb:2"}]}`,
	} {
		if _, err := ParseList([]byte(bad)); err == nil {
			t.Errorf("accepted %.80s", bad)
		}
	}
	dup, err := ParseList([]byte(`{"format":"ppgmods-mod-list","mods":[{"ref":"gb:1"},{"ref":" gb:1 "},{"ref":"ow:cool-guns"}]}`))
	if err != nil || len(dup.Mods) != 2 {
		t.Fatalf("duplicates: %v %+v", err, dup)
	}
}

// Importing leaves installed mods as they are and counts them.
func TestImportListSkipsInstalled(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	st, _ := manager.LoadState()
	st.Mods["gb:1"] = &manager.Installed{Key: "gb:1", Name: "One"}
	m := &manager.Manager{State: st, ModsDir: t.TempDir(), ContraptionsDir: t.TempDir()}
	l, _ := ParseList([]byte(`{"format":"ppgmods-mod-list","mods":[{"ref":"gb:1","off":true}]}`))
	sum := (&App{Opt: DefaultOptions()}).ImportList(m, l)
	if sum.Current != 1 || sum.OK+sum.Failed+sum.Refused != 0 || st.Mods["gb:1"].Off {
		t.Fatalf("summary %+v, off %v", sum, st.Mods["gb:1"].Off)
	}
}
