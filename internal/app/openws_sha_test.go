package app

import (
	"strings"
	"testing"

	"github.com/Trlydev/SWG/internal/workshop"
)

func TestDownloadOWRejectsMalformedChecksum(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	for _, sha := range []string{"", "abc", strings.Repeat("a", 62), strings.Repeat("z", 64)} {
		e := &workshop.Entry{Slug: "sha-test", SHA256: sha,
			File: "https://github.com/" + workshop.Repo + "/releases/download/workshop-files/x.zip"}
		if _, err := downloadOW(e); err == nil || !strings.Contains(err.Error(), "malformed SHA-256") {
			t.Fatalf("SHA256 %q: got %v", sha, err)
		}
	}
}
