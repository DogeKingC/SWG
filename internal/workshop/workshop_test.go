package workshop

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for n, c := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(c))
	}
	zw.Close()
	return b.Bytes()
}

func serve(t *testing.T, body []byte) *httptest.Server {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	t.Cleanup(srv.Close)
	return srv
}

func sub(url string, body []byte) *Submission {
	sum := sha256.Sum256(body)
	return &Submission{Name: "Test", Author: "Me", Kind: "mod", Version: "1.0", Download: url, SHA256: hex.EncodeToString(sum[:]), Maintainers: []string{"me"}}
}

func TestCheck(t *testing.T) {
	good := zipOf(t, map[string]string{"Test/mod.json": `{"Name":"Test","Author":"Me","ModVersion":"1.0"}`, "Test/a.cs": "class A {}"})
	srv := serve(t, good)
	if r := Check(srv.Client(), sub(srv.URL, good), t.TempDir()); len(r.Problems) > 0 || r.Kind != "mod" || r.ScanMax != "none" {
		t.Fatalf("good mod: %+v", r)
	}
	worm := zipOf(t, map[string]string{"W/mod.json": `{"Name":"Test","Author":"Me"}`,
		"W/a.cs": "using Steamworks; class A { void B() { SteamUGC.SubmitItemUpdate(default, \"\"); } }"})
	srv2 := serve(t, worm)
	r := Check(srv2.Client(), sub(srv2.URL, worm), t.TempDir())
	if len(r.Problems) == 0 || !strings.Contains(strings.Join(r.Problems, " "), "worm") {
		t.Fatalf("worm mod: %+v", r)
	}
	contr := zipOf(t, map[string]string{"Tank/Tank.jaap": "x"})
	srv3 := serve(t, contr)
	if r := Check(srv3.Client(), sub(srv3.URL, contr), t.TempDir()); !strings.Contains(strings.Join(r.Problems, " "), "kind") {
		t.Fatalf("contraption submitted as a mod: %+v", r)
	}
}

func TestValidate(t *testing.T) {
	s := &Submission{Name: "X", Author: "Y", Kind: "skin", Version: "1", Download: "http://x/y.zip", SHA256: "abc", Maintainers: []string{"bad name!"}}
	if p := s.Validate(); len(p) < 4 {
		t.Fatalf("problems: %v", p)
	}
	if ValidSlug("../etc") || ValidSlug("A") || !ValidSlug("quick-draw") {
		t.Fatal("slugs")
	}
}
