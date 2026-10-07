package cfclear

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// A real-browser smoke test: a local server that answers with the clearance
// cookie right away stands in for a passed check. Skips unless
// PPGMODS_BROWSER points at a browser, so `go test ./...` never opens a
// window on its own.
func TestRunWithLocalServer(t *testing.T) {
	exe := os.Getenv("PPGMODS_BROWSER")
	if exe == "" {
		t.Skip("set PPGMODS_BROWSER to a browser executable to run this smoke test")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "cf_clearance", Value: "test-clearance", Path: "/"})
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><head><title>Test Page</title></head><body>passed</body></html>")
	}))
	defer srv.Close()

	res, err := Run(srv.URL, Options{Timeout: 60 * time.Second, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cookie("cf_clearance") != "test-clearance" {
		t.Fatalf("clearance cookie not captured: %+v", res.Cookies)
	}
	if res.UserAgent == "" {
		t.Fatal("no User-Agent captured")
	}
}
