package sources

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// A stored clearance is attached on the retry, with the User-Agent it was
// issued to, and the page comes back.
func TestSkyClearanceAttached(t *testing.T) {
	page, err := os.ReadFile("testdata/skymods_page.html")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	requests := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if r.Header.Get("Cookie") == "cf_clearance=good" && r.Header.Get("User-Agent") == "Browser/1.0" {
			w.Write(page)
			return
		}
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(403)
		io.WriteString(w, "Just a moment...")
	})
	defer UseTestServer(stubRT{h}, "http://unused")()
	clearSkyCache()

	oldC, oldE := SkyClearance, SkyClearanceExpired
	defer func() { SkyClearance, SkyClearanceExpired = oldC, oldE }()
	SkyClearance = func() (string, string) { return "good", "Browser/1.0" }
	SkyClearanceExpired = func() { t.Error("a working clearance must not be marked expired") }

	if _, err := SkySearch("sky-clearance-attach", 1); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Fatalf("expected first attempt + clearance retry, got %d requests", requests)
	}
	if SkyChallengeUp() {
		t.Fatal("a successful fetch must clear the challenge marker")
	}
}

// A clearance Cloudflare no longer accepts is reported expired and dropped.
func TestSkyClearanceExpired(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(403)
		io.WriteString(w, "Just a moment...")
	})
	defer UseTestServer(stubRT{h}, "http://unused")()
	clearSkyCache()

	oldC, oldE := SkyClearance, SkyClearanceExpired
	defer func() { SkyClearance, SkyClearanceExpired = oldC, oldE }()
	SkyClearance = func() (string, string) { return "stale", "Browser/1.0" }
	expired := false
	SkyClearanceExpired = func() { expired = true }

	_, err := SkySearch("sky-clearance-expired", 1)
	if !errors.Is(err, ErrSkyChallenge) {
		t.Fatalf("want ErrSkyChallenge, got %v", err)
	}
	if !expired {
		t.Fatal("a rejected clearance must be reported expired")
	}
	if !SkyChallengeUp() {
		t.Fatal("the challenge marker is missing")
	}
	InvalidateSkyCache()
	if SkyChallengeUp() {
		t.Fatal("InvalidateSkyCache must clear the challenge marker")
	}
}

// When Cloudflare refuses ppgmods' own requests, even with the stored
// clearance, the page is read through the browser once, and the request
// does not loop.
func TestSkyBrowserGetHeals(t *testing.T) {
	page, err := os.ReadFile("testdata/skymods_page.html")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(403)
		io.WriteString(w, "Just a moment...")
	})
	defer UseTestServer(stubRT{h}, "http://unused")()
	clearSkyCache()

	oldC, oldE, oldB := SkyClearance, SkyClearanceExpired, SkyBrowserGet
	defer func() { SkyClearance, SkyClearanceExpired, SkyBrowserGet = oldC, oldE, oldB }()
	SkyClearance = func() (string, string) { return "refused", "Browser/2.0" }
	var expired, browser atomic.Int32
	SkyClearanceExpired = func() { expired.Add(1) }
	SkyBrowserGet = func(u string) ([]byte, error) {
		browser.Add(1)
		return page, nil
	}

	items, err := SkySearch("sky-browser-heal", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("the page read through the browser was not parsed")
	}
	if requests.Load() != 2 || expired.Load() != 1 || browser.Load() != 1 {
		t.Fatalf("requests %d, expired %d, browser reads %d; want 2, 1, 1", requests.Load(), expired.Load(), browser.Load())
	}
	if SkyChallengeUp() {
		t.Fatal("a page read through the browser must clear the challenge marker")
	}
}

// A browser read that fails (the check wants a person) ends in
// ErrSkyChallenge carrying the reason, after one try.
func TestSkyBrowserGetFailed(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(403)
		io.WriteString(w, "Just a moment...")
	})
	defer UseTestServer(stubRT{h}, "http://unused")()
	clearSkyCache()

	oldB := SkyBrowserGet
	defer func() { SkyBrowserGet = oldB }()
	var calls atomic.Int32
	SkyBrowserGet = func(string) ([]byte, error) {
		calls.Add(1)
		return nil, errors.New("wants a person")
	}

	_, err := SkySearch("sky-browser-failed", 1)
	if !errors.Is(err, ErrSkyChallenge) || !strings.Contains(err.Error(), "wants a person") {
		t.Fatalf("want ErrSkyChallenge with the reason, got %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the browser read ran %d times, want 1", n)
	}
}
