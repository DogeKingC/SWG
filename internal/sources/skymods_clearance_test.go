package sources

import (
	"errors"
	"io"
	"net/http"
	"os"
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

// When the automatic check succeeds, the request heals by itself: challenge
// → background browser stores a clearance → retry carries it → page served.
func TestSkyAutoCheckHeals(t *testing.T) {
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
		if r.Header.Get("Cookie") == "cf_clearance=fresh" && r.Header.Get("User-Agent") == "Browser/2.0" {
			w.Write(page)
			return
		}
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(403)
		io.WriteString(w, "Just a moment...")
	})
	defer UseTestServer(stubRT{h}, "http://unused")()
	clearSkyCache()

	oldC, oldA := SkyClearance, SkyAutoCheck
	defer func() { SkyClearance, SkyAutoCheck = oldC, oldA }()
	var stored atomic.Bool
	SkyClearance = func() (string, string) {
		if stored.Load() {
			return "fresh", "Browser/2.0"
		}
		return "", ""
	}
	SkyAutoCheck = func() bool {
		stored.Store(true) // the background browser passed the check
		return true
	}

	if _, err := SkySearch("sky-auto-heal", 1); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Fatalf("expected challenge + healed retry, got %d requests", requests)
	}
}

// A check that the background browser cannot pass still ends in the friendly
// error, without spinning on retries.
func TestSkyAutoCheckFailed(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(403)
		io.WriteString(w, "Just a moment...")
	})
	defer UseTestServer(stubRT{h}, "http://unused")()
	clearSkyCache()

	oldA := SkyAutoCheck
	defer func() { SkyAutoCheck = oldA }()
	var autoCalls atomic.Int32
	SkyAutoCheck = func() bool {
		autoCalls.Add(1)
		return false
	}

	_, err := SkySearch("sky-auto-failed", 1)
	if !errors.Is(err, ErrSkyChallenge) {
		t.Fatalf("want ErrSkyChallenge, got %v", err)
	}
	if n := autoCalls.Load(); n != 1 {
		t.Fatalf("the automatic check ran %d times, want 1", n)
	}
}
