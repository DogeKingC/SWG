package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DogeKingC/SWG/internal/workshop"
)

// A folder of old Workshop items, packed on the owner's PC and published by
// the workflow: clean items are published under their author and Workshop
// ID; post-worm and flagged items are skipped; a second run adds nothing.
func TestBulkPackAndPublish(t *testing.T) {
	src := t.TempDir()
	old := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	write := func(id, rel, body string, when time.Time) {
		p := filepath.Join(src, id, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
		os.Chtimes(p, when, when)
	}
	write("1111111", "mod.json", "\xef\xbb\xbf"+`{"Name":"Cool Guns","Author":"Gunsmith","ModVersion":"2.1","Scripts":["s.cs"]}`, old)
	write("1111111", "s.cs", "class S {}", old)
	write("2222222", "Tank.jaap", "jaap", old)
	write("2222222", "Tank.json", `{"DisplayName":"Big Tank"}`, old)
	write("2222222", "Tank.png", "\x89PNG\r\n\x1a\n", old)
	write("3333333", "mod.json", `{"Name":"Late","Author":"x","Scripts":["s.cs"]}`, old)
	write("3333333", "s.cs", "class S {}", time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
	write("4444444", "mod.json", `{"Name":"Evil","Author":"y","Scripts":["s.cs"]}`, old)
	write("4444444", "s.cs", `class S { void M() { System.Diagnostics.Process.Start("calc"); } }`, old)
	write("5555555", "readme.txt", "not a mod", old)

	out := t.TempDir()
	m, err := workshop.PackBulk(src, out, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 4 {
		t.Fatalf("packed %d items, want 4 (the folder without a mod or contraption is left out)", len(m.Items))
	}
	if it := m.Items[0]; it.Name != "Cool Guns" || it.Author != "Gunsmith" || it.Version != "2.1" || it.Kind != "mod" {
		t.Fatalf("mod read wrong: %+v", it)
	}
	if it := m.Items[1]; it.Name != "Big Tank" || it.Kind != "contraption" || !strings.Contains(it.Author, "unknown") {
		t.Fatalf("contraption read wrong: %+v", it)
	}

	srv := httptest.NewTLSServer(http.FileServer(http.Dir(out)))
	defer srv.Close()
	oc := client
	client = srv.Client()
	defer func() { client = oc }()

	ix := &workshop.Index{}
	files := t.TempDir()
	r := bulkPublish(ix, m.Items, srv.URL+"/", "owner", files, 150)
	t.Log(r.summary())
	if len(r.published) != 2 || len(r.skipped) != 2 {
		t.Fatalf("published %d, skipped %d; want 2 and 2", len(r.published), len(r.skipped))
	}
	got := map[string]workshop.Entry{}
	for _, e := range ix.Entries {
		got[e.WorkshopID] = e
	}
	if e := got["1111111"]; e.Author != "Gunsmith" || e.Owner != "owner" || e.Slug != "cool-guns-1111111" || !e.Approved {
		t.Errorf("mod entry: %+v", e)
	}
	if _, ok := got["2222222"]; !ok {
		t.Error("contraption not published")
	}
	if _, ok := got["3333333"]; ok {
		t.Error("an item with post-worm files was published")
	}
	if _, ok := got["4444444"]; ok {
		t.Error("an item that starts a process was published")
	}
	if ents, _ := os.ReadDir(files); len(ents) != 2 {
		t.Errorf("%d files staged for upload, want 2", len(ents))
	}

	r = bulkPublish(ix, m.Items, srv.URL+"/", "owner", files, 150)
	if len(r.published) != 0 || r.present != 2 {
		t.Errorf("second run: published %d, already there %d", len(r.published), r.present)
	}
}
