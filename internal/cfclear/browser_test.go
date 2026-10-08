package cfclear

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Browsers beyond Chrome and Edge are found (here Vivaldi on PATH), the
// PPGMODS_BROWSER override comes first, and the error for no browser says
// that Firefox-based ones like Zen can't be used.
func TestFindBrowsers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PATH lookup test for Linux")
	}
	dir := t.TempDir()
	viv := filepath.Join(dir, "vivaldi")
	os.WriteFile(viv, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PPGMODS_BROWSER", "")
	if got := FindBrowsers(); len(got) == 0 || got[0] != viv {
		t.Fatalf("FindBrowsers = %q, want %s first", got, viv)
	}
	own := filepath.Join(dir, "my-browser")
	os.WriteFile(own, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PPGMODS_BROWSER", own)
	if got, _ := FindBrowser(); got != own {
		t.Fatalf("FindBrowser = %q, want the PPGMODS_BROWSER path", got)
	}
	if !strings.Contains(ErrNoBrowser.Error(), "Zen") {
		t.Fatal("the no-browser message should name Firefox-based browsers like Zen")
	}
}
