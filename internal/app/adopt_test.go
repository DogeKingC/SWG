package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/sources"
)

func TestFindExisting(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	game := t.TempDir()
	mods, contr := filepath.Join(game, "Mods"), filepath.Join(game, "Contraptions")
	write := func(p, body string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(mods, "Quick Draw", "mod.json"), `{"Name":"Quick Draw Mod","Author":"51804","ModVersion":"3.2","CreatorUGCIdentity":"3801154351"}`)
	write(filepath.Join(mods, "Quick Draw", "mod.cs"), `class A {}`)
	write(filepath.Join(mods, "LivingPeople", "mod.json"), `{"Name":"Living People (AI)","Author":"Jack's Heart"}`)
	write(filepath.Join(mods, "Mine", "mod.json"), `{"Name":"My Own Mod"}`)
	write(filepath.Join(mods, "notamod", "readme.txt"), `x`)
	write(filepath.Join(contr, "DestrucHouse", "DestrucHouse.jaap"), `x`)
	write(filepath.Join(contr, "DestrucHouse", "DestrucHouse.json"), `{"Name":"DestrucHouse","DisplayName":"DestrucHouse"}`)

	twCatalogue = func() ([]sources.TWItem, error) {
		return []sources.TWItem{
			{ID: 170, Title: "Living People (AI)", Author: "Jack's Heart", Type: "mod"},
			{ID: 164, Title: "DestrucHouse", Author: "x", Type: "contraption"},
		}, nil
	}
	defer func() { twCatalogue = sources.TWAll }()

	st, _ := manager.LoadState()
	m := &manager.Manager{State: st, ModsDir: mods, ContraptionsDir: contr}
	found, err := (&App{Opt: DefaultOptions()}).FindExisting(m)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"sky:3801154351": "Quick Draw", "tw:170": "LivingPeople", "local:m-mine": "Mine", "tw:164": "DestrucHouse"}
	if len(found) != len(want) {
		t.Fatalf("found %+v", found)
	}
	for key, folder := range want {
		inst := m.State.Mods[key]
		if inst == nil || inst.Folders[0] != folder || !inst.Adopted {
			t.Errorf("%s: %+v", key, inst)
		}
	}
	if m.State.Mods["sky:3801154351"].Version != "3.2" || m.State.Mods["tw:164"].Kind != manager.KindContraption {
		t.Error("version/kind not recorded")
	}
	// Running again finds nothing new, and nothing was moved.
	if again, _ := (&App{Opt: DefaultOptions()}).FindExisting(m); len(again) != 0 {
		t.Fatalf("second run found %+v", again)
	}
	if _, err := os.Stat(filepath.Join(mods, "Quick Draw", "mod.cs")); err != nil {
		t.Fatal("adoption moved files")
	}
	probs, _ := m.Verify()
	for _, p := range probs {
		if p.Bad {
			t.Errorf("verify: %+v", p)
		}
	}
}
