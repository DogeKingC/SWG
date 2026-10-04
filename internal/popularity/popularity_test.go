package popularity

import (
	"testing"
	"time"
)

func TestCompute(t *testing.T) {
	snaps := []Snapshot{
		{Date: "2026-09-01", Sources: []string{"gb", "tw"}, Counts: map[string]int{"gb:1": 100, "gb:2": 0, "tw:1": 5}},
		{Date: "2026-09-25", Sources: []string{"gb"}, Counts: map[string]int{"gb:1": 500, "gb:2": 50}}, // tw failed that day
		{Date: "2026-09-30", Sources: []string{"gb", "tw"}, Counts: map[string]int{"gb:1": 900, "gb:2": 400, "tw:1": 20}},
		{Date: "2026-10-01", Sources: []string{"gb", "tw"}, Counts: map[string]int{"gb:1": 1000, "gb:2": 700, "tw:1": 30, "gb:3": 10}},
	}
	items := []Item{{Ref: "gb:1", Src: "gb", N: 1000}, {Ref: "gb:2", Src: "gb", N: 700}, {Ref: "tw:1", Src: "tw", N: 30}, {Ref: "gb:3", Src: "gb", N: 10}}
	Compute(items, snaps)
	// day: vs 09-30; week: vs 09-01, the newest snapshot on/before 09-24;
	// month: vs 09-01, the oldest (history is shorter than 30 days).
	want := map[string][3]int{
		"gb:1": {100, 900, 900},
		"gb:2": {300, 700, 700},
		"tw:1": {10, 25, 25},
		"gb:3": {10, 10, 10}, // new item: everything counts
	}
	for _, it := range items {
		if got := [3]int{it.D, it.W, it.M}; got != want[it.Ref] {
			t.Errorf("%s: got %v want %v", it.Ref, got, want[it.Ref])
		}
	}
	ix := &Index{Items: items, Generated: time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC), Since: "2026-09-01"}
	top := ix.Query("gb", "", "", "day", 0, 10)
	if len(top) != 3 || top[0].Ref != "gb:2" {
		t.Fatalf("day ranking: %+v", top)
	}
	if ix.Days() != 30 {
		t.Fatalf("days = %d", ix.Days())
	}
}
