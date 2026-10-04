package archive

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
	return p
}

func TestExtractZip(t *testing.T) {
	src := writeZip(t, map[string]string{"Mod/mod.json": "{}", "Mod/script.cs": "class A{}"})
	dest := t.TempDir()
	if err := Extract(src, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Mod", "script.cs")); err != nil {
		t.Fatal(err)
	}
}

func TestZipSlipRejected(t *testing.T) {
	for _, name := range []string{"../evil.cs", "a/../../evil.cs", `..\evil.cs`, "/abs/evil.cs"} {
		src := writeZip(t, map[string]string{name: "x"})
		err := Extract(src, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Errorf("%q: expected traversal error, got %v", name, err)
		}
	}
}

func TestUnknownFormat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.bin")
	os.WriteFile(p, []byte("hello world"), 0o644)
	if err := Extract(p, t.TempDir()); err == nil {
		t.Fatal("expected error")
	}
}

func TestHiddenStreamNameRejected(t *testing.T) {
	src := writeZip(t, map[string]string{"Mod/mod.json": "{}", "Mod/Main.cs:payload": "class Evil{}"})
	if err := Extract(src, t.TempDir()); err == nil || !strings.Contains(err.Error(), "invalid name") {
		t.Fatalf("got %v", err)
	}
}
