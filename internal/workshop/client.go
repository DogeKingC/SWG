package workshop

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"encoding/json"
)

// PublicKey verifies index.sig. While it is empty (no signing key set up
// yet), the index is trusted as served by GitHub over HTTPS; once set, an
// index without a valid signature is refused. Make a key pair with
// `go run ./cmd/workshop keygen`.
var PublicKey = signingPublicKey

var (
	idxMu sync.Mutex
	idx   *Index
	idxAt time.Time
	httpc = &http.Client{Timeout: 60 * time.Second}
	// CacheDir keeps the last index for offline use ("" disables).
	CacheDir string
)

// ErrBadSignature means the index's signature didn't verify.
var ErrBadSignature = errors.New("the Open Workshop index has no valid signature")

// FetchIndex returns the published index (refreshed every 15 minutes),
// read at the data branch's newest commit so a CDN can't serve a stale one.
func FetchIndex() (*Index, error) {
	idxMu.Lock()
	defer idxMu.Unlock()
	if idx != nil && time.Since(idxAt) < 15*time.Minute {
		return idx, nil
	}
	ix, err := download()
	if err == nil {
		idx, idxAt = ix, time.Now()
		return ix, nil
	}
	if errors.Is(err, ErrBadSignature) {
		return nil, err
	}
	if idx != nil {
		return idx, nil
	}
	if CacheDir != "" {
		if b, rerr := os.ReadFile(filepath.Join(CacheDir, "workshop-index.json")); rerr == nil {
			sig, _ := os.ReadFile(filepath.Join(CacheDir, "workshop-index.sig"))
			if ix, perr := parse(b, string(sig)); perr == nil {
				idx, idxAt = ix, time.Now().Add(-10*time.Minute)
				return ix, nil
			}
		}
	}
	return nil, err
}

// CachedIndex returns the last index fetched, without network.
func CachedIndex() *Index {
	idxMu.Lock()
	defer idxMu.Unlock()
	return idx
}

func get(u string) ([]byte, int, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "ppgmods")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return b, resp.StatusCode, err
}

func download() (*Index, error) {
	ref := DataBranch
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+Repo+"/commits/"+DataBranch, nil)
	req.Header.Set("Accept", "application/vnd.github.sha")
	req.Header.Set("User-Agent", "ppgmods")
	if resp, err := httpc.Do(req); err == nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
		resp.Body.Close()
		if sha := strings.TrimSpace(string(b)); resp.StatusCode == 200 && len(sha) == 40 {
			ref = sha
		} else if resp.StatusCode == 404 || resp.StatusCode == 422 {
			return nil, errors.New("the Open Workshop has nothing published yet")
		}
	}
	base := "https://raw.githubusercontent.com/" + Repo + "/" + ref + "/"
	b, code, err := get(base + "index.json")
	if err != nil {
		return nil, err
	}
	if code == 404 {
		return nil, errors.New("the Open Workshop has nothing published yet")
	}
	if code != 200 {
		return nil, fmt.Errorf("Open Workshop index: HTTP %d", code)
	}
	sig, _, _ := get(base + "index.sig")
	ix, err := parse(b, string(sig))
	if err != nil {
		return nil, err
	}
	if CacheDir != "" {
		os.MkdirAll(CacheDir, 0o755)
		os.WriteFile(filepath.Join(CacheDir, "workshop-index.json"), b, 0o644)
		os.WriteFile(filepath.Join(CacheDir, "workshop-index.sig"), sig, 0o644)
	}
	return ix, nil
}

func parse(b []byte, sig string) (*Index, error) {
	if PublicKey != "" && !Verify(b, sig, PublicKey) {
		return nil, ErrBadSignature
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, err
	}
	return &ix, nil
}

// Page is a submission's page on GitHub.
func Page(slug string) string {
	return "https://github.com/" + Repo + "/tree/main/workshop/submissions/" + slug
}

// SetTransport routes the index client through rt (tests). It returns a
// function that undoes it and drops the cached index.
func SetTransport(rt http.RoundTripper) func() {
	idxMu.Lock()
	old := httpc.Transport
	httpc.Transport, idx = rt, nil
	idxMu.Unlock()
	return func() {
		idxMu.Lock()
		httpc.Transport, idx = old, nil
		idxMu.Unlock()
	}
}

// ForgetIndex drops the cached index (tests, and after publishing).
func ForgetIndex() {
	idxMu.Lock()
	idx = nil
	idxMu.Unlock()
}

// SetIndexForTest makes FetchIndex return ix (tests).
func SetIndexForTest(ix *Index) func() {
	idxMu.Lock()
	old, oldAt := idx, idxAt
	idx, idxAt = ix, time.Now().Add(100*365*24*time.Hour)
	idxMu.Unlock()
	return func() {
		idxMu.Lock()
		idx, idxAt = old, oldAt
		idxMu.Unlock()
	}
}
