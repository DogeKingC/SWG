//go:build cgo && !nofyne

package gui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/test"
)

// The details sheet fills in its facts after it is shown. The parts below
// them must move down, not be drawn over them (the "Safety check" heading
// and verdict on top of the Source/Size/ID tiles).
func TestDetailsSheetMakesRoomForFacts(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		a := test.NewTempApp(t)
		a.Settings().SetTheme(newTheme())
		d := &detailsWin{facts: flowBox(8), check: tight(muted("Downloading and scanning before install…"))}
		section := tight(sectionTitle("Safety check"), d.check)
		d.body = container.New(&vlist{gap: 14}, muted("Lite Pixel Blood V5"), d.facts, section)
		d.scroll = container.NewVScroll(container.New(layout.NewCustomPaddedLayout(16, 16, 22, 22), d.body))
		w := test.NewTempWindow(t, d.scroll)
		w.Resize(fyne.NewSize(860, 700))

		for _, kv := range [][2]string{{"Source", "Open Workshop"}, {"Size", "1.2 MB"}, {"ID", "ow:lite-pixel-blood"}} {
			d.facts.Add(fact(kv[0], kv[1]))
		}
		d.facts.Refresh()
		if fixed {
			d.relayout()
		}
		// The tiles are drawn from the row's top down, whatever height the
		// row was given: they end at its top plus the height they need.
		factsBottom := d.facts.Position().Y + d.facts.MinSize().Height
		overlap := section.Position().Y < factsBottom
		switch {
		case fixed && overlap:
			t.Errorf("Safety check starts at %v, inside the facts (end at %v)", section.Position().Y, factsBottom)
		case !fixed && overlap:
			t.Logf("without relayout (the bug): Safety check at %v, over the facts that end at %v", section.Position().Y, factsBottom)
		case !fixed:
			t.Errorf("could not reproduce the overlap without relayout (section %v, facts end %v)", section.Position().Y, factsBottom)
		default:
			t.Logf("with relayout: Safety check at %v, below the facts that end at %v", section.Position().Y, factsBottom)
		}
	}
}
