package cfclear

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Browsers beyond Chrome and Edge are found (here Vivaldi on PATH), the
// PPGMODS_BROWSER override comes first, and a Firefox-based browser (Zen)
// is used only when no Chromium-based one is installed.
func TestFindBrowsers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PATH lookup test for Linux")
	}
	sysRoot = t.TempDir() // ignore browsers installed on this machine
	defer func() { sysRoot = "" }()
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
		t.Fatal("the no-browser message should name Zen")
	}

	zdir := t.TempDir()
	zen := filepath.Join(zdir, "zen")
	os.WriteFile(zen, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PPGMODS_BROWSER", "")
	t.Setenv("PATH", zdir+string(os.PathListSeparator)+dir)
	if got, _ := FindBrowser(); got != viv {
		t.Fatalf("FindBrowser = %q, want the Chromium-based %s before Zen", got, viv)
	}
	t.Setenv("PATH", zdir)
	if got, err := FindBrowser(); err != nil || got != zen || !isFirefox(got) {
		t.Fatalf("FindBrowser = %q, %v; want Zen when no Chromium-based browser is installed", got, err)
	}
}
