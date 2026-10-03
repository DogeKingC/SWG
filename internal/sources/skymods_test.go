package sources

import (
	"os"
	"testing"
	"time"
)

func TestParseSkyPage(t *testing.T) {
	b, err := os.ReadFile("testdata/skymods_page.html")
	if err != nil {
		t.Fatal(err)
	}
	items := ParseSkyPage(string(b))
	if len(items) != 3 {
		t.Fatalf("got %d items", len(items))
	}
	it := items[0]
	if it.Title != "Arracourt France 1944" || it.WorkshopID != "3540040431" || it.DownloadURL == "" || it.Author != "pablishen" {
		t.Fatalf("bad parse: %+v", it)
	}
	if want := time.Date(2025, 8, 2, 4, 15, 0, 0, time.UTC); !it.Revision.Equal(want) {
		t.Fatalf("revision %v, want %v", it.Revision, want)
	}
}

func TestParseSteamDateWithoutYear(t *testing.T) {
	ref := time.Date(2026, 9, 21, 17, 0, 0, 0, time.UTC)
	if got := ParseSteamDate("20 Sep at 12:04 UTC", ref); !got.Equal(time.Date(2026, 9, 20, 12, 4, 0, 0, time.UTC)) {
		t.Fatalf("got %v", got)
	}
	// A December revision seen in a January mirror belongs to the previous year.
	ref = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	if got := ParseSteamDate("30 Dec at 10:00 UTC", ref); got.Year() != 2025 {
		t.Fatalf("got %v", got)
	}
}
