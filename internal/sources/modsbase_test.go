package sources

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModsbaseCode(t *testing.T) {
	if c := ModsbaseCode("https://modsbase.com/pqaqvfltqbqn/3801154351_Quick_Draw_Mod.zip.html"); c != "pqaqvfltqbqn" {
		t.Fatalf("got %q", c)
	}
	if c := ModsbaseCode("https://evil.example/pqaqvfltqbqn/x.zip.html"); c != "" {
		t.Fatalf("accepted foreign host: %q", c)
	}
}

func TestParseModsbaseLink(t *testing.T) {
	page := `<div class="download-details"><p>File: x.zip</p><a class="btn" href="https://dl.modsbase.com/d/abc/3801154351_Quick_Draw_Mod.zip?x=1&amp;y=2">Download</a></div>`
	got, err := ParseModsbaseLink(page)
	if err != nil || got != "https://dl.modsbase.com/d/abc/3801154351_Quick_Draw_Mod.zip?x=1&y=2" {
		t.Fatalf("got %q, %v", got, err)
	}
	cur := `<div class="dl2-expire">valid 2 hours</div> <a class="dl2-btn" href="https://mbuploads.s3.example/uploads/00451/pqaqvfltqbqn?a=1&amp;b=2">Download</a>`
	if got, err := ParseModsbaseLink(cur); err != nil || got != "https://mbuploads.s3.example/uploads/00451/pqaqvfltqbqn?a=1&b=2" {
		t.Fatalf("current layout: got %q, %v", got, err)
	}
	if _, err := ParseModsbaseLink(`<div class="download-details"><a href="javascript:alert(1)">x</a></div>`); err == nil {
		t.Fatal("accepted non-http link")
	}
	if _, err := ParseModsbaseLink(`<html>no link</html>`); err == nil {
		t.Fatal("expected error")
	}
}

func TestPublicIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.1", "172.16.0.1", "169.254.169.254", "::1", "fe80::1", "100.64.0.1", "0.0.0.0"} {
		if PublicIP(net.ParseIP(s)) {
			t.Errorf("%s counted as public", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "140.82.112.3", "2606:4700::1111"} {
		if !PublicIP(net.ParseIP(s)) {
			t.Errorf("%s counted as non-public", s)
		}
	}
}

func TestDownloadsRefuseLocalAddresses(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	c := &http.Client{Transport: publicTransport()}
	if resp, err := c.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("connected to a loopback address")
	}
}
