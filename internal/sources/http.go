// Package sources talks to the mod mirrors: GameBanana (full API, direct
// downloads, server-side AV results) and Skymods (catalogue of Steam Workshop
// copies whose files live on modsbase.com behind a Cloudflare check).
package sources

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"
)

var UserAgent = "ppgmods/dev (+https://github.com/DogeKingC/SWG)"

// client keeps cookies between requests, as a browser does: modsbase.com's
// "create download link" step can depend on cookies set by its file page.
var client = func() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Timeout: 5 * time.Minute, Jar: jar}
}()

// setHeaders adds the identifying User-Agent and the standard headers every
// browser sends.
func setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
}

func get(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	setHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

// Download saves url to path and returns its MD5 and SHA-256 hex digests.
func Download(url, path string, maxBytes int64) (md5hex, sha256hex string, err error) {
	resp, err := get(url)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	f, err := os.Create(path)
	if err != nil {
		return "", "", err
	}
	md5hex, sha256hex, err = saveHashed(f, resp.Body, maxBytes)
	if err != nil {
		os.Remove(path)
	}
	return md5hex, sha256hex, err
}

// saveHashed copies at most maxBytes from r into f, closes f and returns the
// MD5 and SHA-256 of what was written.
func saveHashed(f *os.File, r io.Reader, maxBytes int64) (string, string, error) {
	m, s := md5.New(), sha256.New()
	n, err := io.Copy(io.MultiWriter(f, m, s), io.LimitReader(r, maxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxBytes {
		err = fmt.Errorf("download larger than %d bytes", maxBytes)
	}
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(m.Sum(nil)), hex.EncodeToString(s.Sum(nil)), nil
}
