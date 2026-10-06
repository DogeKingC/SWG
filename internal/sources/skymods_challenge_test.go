package sources

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

// stubRT answers every request (whatever its URL) with h: skyGet talks to a
// fixed host, so the test server is reached by replacing the transport.
type stubRT struct{ h http.Handler }

func (s stubRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func clearSkyCache() {
	skyCacheMu.Lock()
	skyCache = map[string]skyCached{}
	skyCacheMu.Unlock()
}

func TestSkyChallengeCached(t *testing.T) {
	var hits atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(403)
		io.WriteString(w, "<html><title>Just a moment...</title></html>")
	})
	defer UseTestServer(stubRT{h}, "http://unused")()
	clearSkyCache()

	// A challenge comes back as ErrSkyChallenge, not a bare HTTP 403.
	_, err := SkySearch("sky-challenge-test", 1)
	if !errors.Is(err, ErrSkyChallenge) {
		t.Fatalf("want ErrSkyChallenge, got %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("challenge hit the server %d times", n)
	}
	// It is cached: a repeat within the cache window does not ask Cloudflare
	// again (every search must not hammer a blocked site).
	if _, err := SkySearch("sky-challenge-test", 1); !errors.Is(err, ErrSkyChallenge) {
		t.Fatalf("cached challenge lost: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("second search hit the server again (%d times)", n)
	}

	// A plain 403 with the challenge body (no header) is also detected.
	_, err = SkyLatest(9)
	if !errors.Is(err, ErrSkyChallenge) {
		t.Fatalf("body-only challenge not detected: %v", err)
	}
}

func TestSkyChallengeThenRecovery(t *testing.T) {
	var challenge atomic.Bool
	page, err := os.ReadFile("testdata/skymods_page.html")
	if err != nil {
		t.Fatal(err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if challenge.Load() {
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(403)
			io.WriteString(w, "Just a moment...")
			return
		}
		w.Write(page)
	})
	defer UseTestServer(stubRT{h}, "http://unused")()
	clearSkyCache()

	challenge.Store(true)
	if _, err := SkySearch("sky-recovery-test", 1); !errors.Is(err, ErrSkyChallenge) {
		t.Fatalf("want ErrSkyChallenge, got %v", err)
	}
	// Still cached as a challenge even though the site has recovered...
	challenge.Store(false)
	if _, err := SkySearch("sky-recovery-test", 1); !errors.Is(err, ErrSkyChallenge) {
		t.Fatalf("challenge cache lost: %v", err)
	}
	// ...but a different page is fetched and parsed normally.
	items, err := SkySearch("sky-recovery-test-2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || items[0].WorkshopID != "3540040431" {
		t.Fatalf("items: %+v", items)
	}
}
