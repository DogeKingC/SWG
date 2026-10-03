package game

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindGameDirLinuxDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("linux layout")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PPG_DIR", "")
	gameDir := filepath.Join(home, ".local", "share", "Steam", "steamapps", "common", "People Playground")
	if err := os.MkdirAll(gameDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindGameDir("")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(gameDir); got != gameDir && got != want {
		t.Fatalf("got %s, want %s", got, gameDir)
	}
	if mods := ModsDir(got); filepath.Base(mods) != "Mods" {
		t.Fatalf("mods dir %s", mods)
	}
}

func TestFindGameDirSecondLibrary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("linux layout")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PPG_DIR", "")
	root := filepath.Join(home, ".local", "share", "Steam")
	lib := filepath.Join(home, "games", "SteamLibrary")
	os.MkdirAll(filepath.Join(root, "steamapps"), 0o755)
	os.MkdirAll(filepath.Join(lib, "steamapps", "common", "People Playground"), 0o755)
	vdf := "\"libraryfolders\"\n{\n\t\"0\"\n\t{\n\t\t\"path\"\t\t\"" + root + "\"\n\t}\n\t\"1\"\n\t{\n\t\t\"path\"\t\t\"" + lib + "\"\n\t}\n}\n"
	os.WriteFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"), []byte(vdf), 0o644)
	got, err := FindGameDir("")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(filepath.Dir(got))) != "steamapps" || filepath.Base(got) != "People Playground" {
		t.Fatalf("got %s", got)
	}
}
