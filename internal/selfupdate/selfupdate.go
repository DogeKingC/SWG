// Package selfupdate checks GitHub Releases for a newer ppgmods build and
// replaces the running executable with it after verifying its SHA-256
// against the release's SHA256SUMS.txt.
package selfupdate

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const Repo = "DogeKingC/SWG"

// Current is the running version (set from main's -ldflags version).
var Current = "dev"

var client = &http.Client{Timeout: 10 * time.Minute}

type Release struct {
	Version  string `json:"version"`
	URL      string `json:"url"`
	Notes    string `json:"notes"`
	Newer    bool   `json:"newer"`
	asset    string
	sumsURL  string
	assetURL string
}

type ghRelease struct {
	Tag    string `json:"tag_name"`
	URL    string `json:"html_url"`
	Body   string `json:"body"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// AssetName is the release file for this OS/architecture.
func AssetName() string {
	n := fmt.Sprintf("ppgmods-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

// Latest fetches the newest published release.
func Latest() (*Release, error) {
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+Repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ppgmods/"+Current)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, errors.New("no published release found (the repository may be private or have no releases yet)")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}
	var gr ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, err
	}
	r := &Release{Version: gr.Tag, URL: gr.URL, Notes: gr.Body, asset: AssetName()}
	for _, a := range gr.Assets {
		switch a.Name {
		case r.asset:
			r.assetURL = a.URL
		case "SHA256SUMS.txt":
			r.sumsURL = a.URL
		}
	}
	r.Newer = Newer(gr.Tag, Current)
	return r, nil
}

// Newer reports whether version a is newer than b. Versions look like
// v1.2.3; "dev" builds never auto-update.
func Newer(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka || !okb {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func download(url string, w io.Writer) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "ppgmods/"+Current)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, 200<<20))
	return err
}

func expectedSum(sumsURL, asset string) (string, error) {
	var b strings.Builder
	if err := download(sumsURL, &b); err != nil {
		return "", err
	}
	sc := bufio.NewScanner(strings.NewReader(b.String()))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s not listed in SHA256SUMS.txt", asset)
}

// Apply downloads the release build for this platform, checks it against
// SHA256SUMS.txt and swaps it in for the running executable. The previous
// executable is kept as <name>.old until the next start.
func Apply(r *Release, logf func(string, ...any)) error {
	if r.assetURL == "" || r.sumsURL == "" {
		return fmt.Errorf("release %s has no %s or SHA256SUMS.txt", r.Version, r.asset)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	want, err := expectedSum(r.sumsURL, r.asset)
	if err != nil {
		return err
	}
	logf("downloading %s %s", r.asset, r.Version)
	tmp := exe + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w", exe, err)
	}
	h := sha256.New()
	err = download(r.assetURL, io.MultiWriter(f, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(tmp)
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", r.asset, want, got)
	}
	logf("checksum OK")
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(old, exe)
		return err
	}
	logf("updated to %s", r.Version)
	return nil
}

// Cleanup removes the executable left behind by a previous update.
func Cleanup() {
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe + ".old")
	}
}
