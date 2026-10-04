package sources

import (
	"os"
	"testing"
	"time"
)

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseTMItem(t *testing.T) {
	it := ParseTMItem(read(t, "testdata/topmods_item.html"), "https://top-mods.com/mods/people-playground/gameplay/4482-greenbrick-industries-r-e-u-p-l-o-a-d.html")
	if it.ID != "4482" || it.WorkshopID != "2822078770" || it.Version != "17.06.22" || it.Author != "Nemesis10033" || it.Size != "9.1 mb" {
		t.Fatalf("%+v", it)
	}
	if !it.VersionTime.Equal(time.Date(2022, 6, 17, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("version time %v", it.VersionTime)
	}
	if it.Title != "Greenbrick Industries(R E U P L O A D)" || it.Image == "" || it.Description == "" {
		t.Fatalf("title %q image %q desc %q", it.Title, it.Image, it.Description)
	}
	if len(it.Downloads) != 2 || it.Downloads[0] != "https://modsfire.com/76v9r5953PlJTQ9" {
		t.Fatalf("downloads %v", it.Downloads)
	}
}

func TestParseTMSearchOnlyPPG(t *testing.T) {
	got := ParseTMSearch(read(t, "testdata/topmods_search.html"))
	if len(got) < 10 {
		t.Fatalf("got %d results", len(got))
	}
	withImg := 0
	for _, s := range got {
		if s.Image != "" {
			withImg++
		}
	}
	if withImg < len(got)/2 {
		t.Fatalf("only %d of %d results have images", withImg, len(got))
	}
	for _, s := range ParseTMSearch(read(t, "testdata/topmods_search_mixed.html")) {
		if s.ID == "" || s.URL == "" {
			t.Fatalf("bad %+v", s)
		}
	}
}

func TestParseTMVersion(t *testing.T) {
	for in, want := range map[string]time.Time{
		"17.06.22":   time.Date(2022, 6, 17, 0, 0, 0, 0, time.UTC),
		"19.09.2026": time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC),
		"v1.2":       {},
	} {
		if got := ParseTMVersion(in); !got.Equal(want) {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestParseTMList(t *testing.T) {
	got := tmBlocks(read(t, "testdata/topmods_list.html"), `<div class="content_list_item mods_list_item">`)
	if len(got) < 8 {
		t.Fatalf("got %d items", len(got))
	}
	if got[0].Image == "" || got[0].Title == "" {
		t.Fatalf("%+v", got[0])
	}
}

func TestTMListViews(t *testing.T) {
	b, err := os.ReadFile("testdata/topmods_list.html")
	if err != nil {
		t.Fatal(err)
	}
	list := tmBlocks(string(b), `<div class="content_list_item mods_list_item">`)
	views := 0
	for _, s := range list {
		views += s.Views
	}
	if len(list) == 0 || views == 0 {
		t.Fatalf("no views parsed from %d items", len(list))
	}
}
