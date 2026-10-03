package sources

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrChallenge means modsbase.com answered with a Cloudflare check instead of
// the page. ppgmods does not try to get past it; the caller falls back to the
// browser.
var ErrChallenge = errors.New("modsbase.com answered with a Cloudflare check")

var (
	reMBCode = regexp.MustCompile(`^https://modsbase\.com/([a-z0-9]{8,16})/`)
	// The current site puts the link on a.dl2-btn; older pages (and the
	// community userscript) use .download-details a.
	reMBLinks = []*regexp.Regexp{
		regexp.MustCompile(`<a[^>]+class="[^"]*\bdl2-btn\b[^"]*"[^>]+href="([^"]+)"`),
		regexp.MustCompile(`<a[^>]+href="([^"]+)"[^>]+class="[^"]*\bdl2-btn\b`),
		regexp.MustCompile(`(?s)download-details.*?<a[^>]+href="([^"]+)"`),
	}
	reMBWait = regexp.MustCompile(`id="countdown"[^>]*data-total="(\d{1,2})"`)
)

// ModsbaseCode returns the file code from a modsbase.com file page URL.
func ModsbaseCode(page string) string { return first(reMBCode, page) }

func isChallenge(resp *http.Response, body []byte) bool {
	if resp.Header.Get("cf-mitigated") == "challenge" {
		return true
	}
	return resp.StatusCode == 403 && strings.Contains(string(body), "Just a moment")
}

// ModsbaseResolve runs modsbase's "Create download link" step (the same form
// post the site's own button makes) and returns the generated file link. A
// failed attempt is retried once; a Cloudflare check is not retried.
func ModsbaseResolve(page string) (string, error) {
	link, err := modsbaseResolveOnce(page)
	if err == nil || errors.Is(err, ErrChallenge) {
		return link, err
	}
	time.Sleep(3 * time.Second)
	return modsbaseResolveOnce(page)
}

func modsbaseResolveOnce(page string) (string, error) {
	code := ModsbaseCode(page)
	if code == "" {
		return "", fmt.Errorf("not a modsbase file page: %s", page)
	}
	if err := modsbaseCountdown(page); err != nil {
		return "", err
	}
	form := url.Values{"op": {"download2"}, "id": {code}, "rand": {""}, "referer": {""}, "method_free": {""}, "method_premium": {""}}
	req, err := http.NewRequest("POST", "https://modsbase.com/", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	setHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", page)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if isChallenge(resp, body) {
		return "", ErrChallenge
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("modsbase.com: HTTP %d", resp.StatusCode)
	}
	return ParseModsbaseLink(string(body))
}

// modsbaseCountdown loads the file page like a browser would and waits out
// the site's "Wait N seconds" countdown before the link is requested.
func modsbaseCountdown(page string) error {
	req, err := http.NewRequest("GET", page, nil)
	if err != nil {
		return err
	}
	setHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if isChallenge(resp, body) {
		return ErrChallenge
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("modsbase.com: HTTP %d", resp.StatusCode)
	}
	if strings.Contains(string(body), "data-sitekey") {
		return errors.New("modsbase.com asks for a captcha")
	}
	wait := 5
	if m := reMBWait.FindStringSubmatch(string(body)); m != nil {
		wait, _ = strconv.Atoi(m[1])
	}
	time.Sleep(time.Duration(wait+1) * time.Second)
	return nil
}

// ParseModsbaseLink extracts the generated file link from the response page.
func ParseModsbaseLink(page string) (string, error) {
	var link string
	for _, re := range reMBLinks {
		if link = html.UnescapeString(first(re, page)); link != "" {
			break
		}
	}
	u, err := url.Parse(link)
	if link == "" || err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return "", errors.New("modsbase.com did not return a download link")
	}
	return link, nil
}

// ModsbaseDownload fetches a generated file link into path and returns its
// SHA-256.
func ModsbaseDownload(link, page, path string, maxBytes int64) (string, error) {
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return "", err
	}
	setHeaders(req)
	req.Header.Set("Referer", page)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if isChallenge(resp, head) {
			return "", ErrChallenge
		}
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		return "", errors.New("download link returned a web page, not a file")
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	_, sum, err := saveHashed(f, resp.Body, maxBytes)
	if err != nil {
		os.Remove(path)
	}
	return sum, err
}
