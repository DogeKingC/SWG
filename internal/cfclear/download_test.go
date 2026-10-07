package cfclear

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The background browser only passes a check: a page (or an ad on it) must
// not be able to put a file into Downloads, where the browser fallback picks
// up new archives to install. Needs Chrome, Edge or Chromium on this PC.
func TestBackgroundBrowserCannotDownload(t *testing.T) {
	exe, err := FindBrowser()
	if err != nil {
		t.Skip("no browser on this machine:", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/payload.zip" {
			w.Header().Set("Content-Disposition", "attachment; filename=payload.zip")
			w.Write([]byte("PK\x03\x04 pushed by an ad"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<title>Just a moment...</title><script>location.href="/payload.zip"</script>`))
	}))
	defer srv.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	Run(srv.URL+"/", Options{Browser: exe, Timeout: 8 * time.Second})
	time.Sleep(time.Second)
	filepath.Walk(home, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && filepath.Ext(p) == ".zip" {
			t.Errorf("the page put %s on disk", p)
		}
		return nil
	})
}
