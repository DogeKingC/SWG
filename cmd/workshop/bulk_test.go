package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Trlydev/SWG/internal/workshop"
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
	m, err := workshop.PackBulk(src, out, "", t.Logf)
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
	r := bulkPublish(ix, m.Items, srv.URL+"/", "owner", files, 150, nil)
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

	r = bulkPublish(ix, m.Items, srv.URL+"/", "owner", files, 150, nil)
	if len(r.published) != 0 || r.present != 2 {
		t.Errorf("second run: published %d, already there %d", len(r.published), r.present)
	}
}

// Old Workshop items often carry project files, editor sources, copies of
// the game's own DLLs and a mod.json with comments or trailing commas.
// None of that runs in game: the pack leaves it out, CI reads the loose
// mod.json, and the item is published. A DLL that differs from the game's
// copy stays in and holds the item.
func TestBulkPackLeavesOutDevFiles(t *testing.T) {
	src, managed := t.TempDir(), t.TempDir()
	old := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	write := func(root, rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
		os.Chtimes(p, old, old)
	}
	unity := "MZ\x90\x00 unity engine"
	write(managed, "UnityEngine.CoreModule.dll", unity)
	write(src, "6666666/s.cs", "class S {}")
	write(src, "6666666/mod.json", "// my mod\n{\n  \"Name\": \"Loose\", /* old */ \"Author\": \"Old Timer\",\n  \"Scripts\": [\"s.cs\", \"Scripts\\\\obj\\\\Gun.cs\",],\n}\n")
	write(src, "6666666/scripts/obj/Gun.cs", "class Gun {}")
	for _, f := range []string{"Loose.csproj", "Loose.sln", "s.pdb", "Loose.csproj.user", ".gitattributes",
		"packages.config", "art/gun.aseprite", "art/gun.pdn", "obj/Debug/x.dll", "bin/s.dll"} {
		write(src, "6666666/"+f, "dev")
	}
	write(src, "6666666/UnityEngine.CoreModule.dll", unity)
	write(src, "7777777/mod.json", `{"Name":"Other","Author":"z","Scripts":["s.cs"]}`)
	write(src, "7777777/s.cs", "class S {}")
	write(src, "7777777/UnityEngine.CoreModule.dll", "MZ\x90\x00 not the game's")

	out := t.TempDir()
	m, err := workshop.PackBulk(src, out, managed, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 2 {
		t.Fatalf("packed %d items, want 2", len(m.Items))
	}
	if it := m.Items[0]; it.Name != "Loose" || it.Author != "Old Timer" || len(it.Removed) != 11 || slices.Contains(it.Removed, "scripts/obj/") {
		t.Fatalf("loose item: %+v", it)
	}
	if it := m.Items[1]; len(it.Removed) != 0 {
		t.Fatalf("a DLL that differs from the game's was left out: %+v", it.Removed)
	}

	srv := httptest.NewTLSServer(http.FileServer(http.Dir(out)))
	defer srv.Close()
	oc := client
	client = srv.Client()
	defer func() { client = oc }()
	ix := &workshop.Index{}
	r := bulkPublish(ix, m.Items, srv.URL+"/", "owner", t.TempDir(), 150, nil)
	t.Log(r.summary())
	if len(r.published) != 1 || len(ix.Entries) != 1 || ix.Entries[0].WorkshopID != "6666666" {
		t.Fatalf("want only the loose item published; got %d published, %d skipped", len(r.published), len(r.skipped))
	}
}

// An item held only for the owner's decision goes to the review queue with
// its findings, not to the bin: review lists it, approve-held publishes it
// after checking it again, and a file that changed in the meantime is
// refused. Items with hard problems never enter the queue.
func TestReviewQueue(t *testing.T) {
	src := t.TempDir()
	old := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	write := func(rel, body string) {
		p := filepath.Join(src, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
		os.Chtimes(p, old, old)
	}
	write("4444444/mod.json", `{"Name":"Launcher","Author":"y","Scripts":["s.cs"]}`)
	write("4444444/s.cs", `class S { void M() { System.Diagnostics.Process.Start("calc"); } }`)
	write("5555555/mod.json", `{"Name":"Late","Author":"z","Scripts":["s.cs"]}`)
	write("5555555/s.cs", "class S {}")
	os.Chtimes(filepath.Join(src, "5555555", "s.cs"), time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
	out := t.TempDir()
	m, err := workshop.PackBulk(src, out, "", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.FileServer(http.Dir(out)))
	defer srv.Close()
	oc := client
	client = srv.Client()
	defer func() { client = oc }()

	data := t.TempDir()
	ix := &workshop.Index{}
	held := &heldList{}
	r := bulkPublish(ix, m.Items, srv.URL+"/", "owner", t.TempDir(), 150, held)
	if len(r.published) != 0 || r.held != 1 || len(held.Items) != 1 || held.Items[0].WorkshopID != "4444444" {
		t.Fatalf("published %d, held %d: %+v", len(r.published), r.held, held.Items)
	}
	if !strings.Contains(r.summary(), "approve-held") {
		t.Error("the bulk summary doesn't point to the review queue")
	}
	if err := saveHeld(data, held); err != nil {
		t.Fatal(err)
	}
	back, _ := loadHeld(data)
	sum := reviewSummary(back)
	if !strings.Contains(sum, "4444444") || !strings.Contains(sum, "Safety scan") || strings.Contains(sum, "5555555") {
		t.Fatalf("review summary:\n%s", sum)
	}

	files := t.TempDir()
	if line := publishHeld(ix, back, "5555555", "owner", files); !strings.Contains(line, "not in the review queue") {
		t.Errorf("an item with a hard problem was publishable: %s", line)
	}
	// The file changed on the release since it was held: refused.
	good, _ := os.ReadFile(filepath.Join(out, "4444444.zip"))
	os.WriteFile(filepath.Join(out, "4444444.zip"), append(append([]byte{}, good...), 0), 0o644)
	if line := publishHeld(ix, back, "4444444", "owner", files); !strings.Contains(line, "NOT published") || len(ix.Entries) != 0 {
		t.Fatalf("a changed file was published: %s", line)
	}
	os.WriteFile(filepath.Join(out, "4444444.zip"), good, 0o644)
	if line := publishHeld(ix, back, "4444444", "owner", files); !strings.Contains(line, "published as") {
		t.Fatalf("approve: %s", line)
	}
	if len(ix.Entries) != 1 || !ix.Entries[0].Approved || len(ix.Entries[0].Findings) == 0 || len(back.Items) != 0 {
		t.Fatalf("entry %+v, queue %+v", ix.Entries, back.Items)
	}
	if err := saveHeld(data, back); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "held.json")); !os.IsNotExist(err) {
		t.Error("an empty queue left held.json behind")
	}
}
