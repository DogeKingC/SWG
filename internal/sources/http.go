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
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"syscall"
	"time"
)

var UserAgent = "ppgmods/dev (+https://github.com/Trlydev/SWG)"

// client keeps cookies between requests, as a browser does: modsbase.com's
// "create download link" step can depend on cookies set by its file page.
var client = func() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Timeout: 5 * time.Minute, Jar: jar, Transport: publicTransport()}
}()

// publicTransport only connects to public internet addresses. Download
// links come from scraped pages; a hijacked mirror must not be able to make
// ppgmods send requests to this computer or the local network (router admin
// pages and the like), directly or through a redirect. With a proxy
// configured, the proxy resolves names and the check is left to it.
func publicTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if !proxyConfigured() {
		d.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !PublicIP(ip) {
				return fmt.Errorf("refusing to connect to non-public address %s", host)
			}
			return nil
		}
	}
	t.DialContext = d.DialContext
	return t
}

func proxyConfigured() bool {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// PublicIP reports whether ip is a routable internet address.
func PublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		ip.Equal(net.IPv4bcast) || ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) // 100.64.0.0/10 (CGNAT)
}

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

func createFile(p string) (*os.File, error) { return os.Create(p) }
func removeFile(p string)                   { os.Remove(p) }

// UseTestServer points the sources at a test server: requests go through rt
// and the Nexus APIs to base. It returns a function that undoes it.
func UseTestServer(rt http.RoundTripper, base string) func() {
	oldT, oldV1, oldQL := client.Transport, NXV1, nxGraphQL
	client.Transport, NXV1, nxGraphQL = rt, base+"/v1", base+"/v2/graphql"
	return func() { client.Transport, NXV1, nxGraphQL = oldT, oldV1, oldQL }
}
