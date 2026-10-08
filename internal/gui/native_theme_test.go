//go:build cgo && !nofyne

package gui

import (
	"testing"

	"fyne.io/fyne/v2/theme"
)

// The native window shows the Appearance it started with: dark by default,
// light when chosen, and the system's variant for "Match system".
func TestWindowThemeVariant(t *testing.T) {
	defer func() { windowTheme = "dark" }()
	for mode, want := range map[string][2]any{
		"dark":   {theme.VariantDark, theme.VariantDark},
		"light":  {theme.VariantLight, theme.VariantLight},
		"system": {theme.VariantLight, theme.VariantDark},
	} {
		windowTheme = mode
		if got := variant(theme.VariantLight); got != want[0] {
			t.Errorf("%s with a light system: %v", mode, got)
		}
		if got := variant(theme.VariantDark); got != want[1] {
			t.Errorf("%s with a dark system: %v", mode, got)
		}
	}
	windowTheme = "light"
	if c := newTheme().Color(theme.ColorNameBackground, theme.VariantDark); c != lightPal.bg {
		t.Errorf("light setting on a dark system: background %v, want the light one", c)
	}
}
