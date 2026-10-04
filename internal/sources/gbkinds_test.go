package sources

import "testing"

func TestClassifyFiles(t *testing.T) {
	cases := map[string][]string{
		KindContraption: {"T-90/T-90.jaap", "T-90/T-90.json", "T-90/T-90.png"},
		KindMod:         {"BetterInspector/mod.json", "BetterInspector/A.cs", "Extra/house.jaap"},
		KindOther:       {"skin/texture.png", "readme.txt"},
		"":              {"inner.zip"},
	}
	for want, names := range cases {
		if got := ClassifyFiles(names); got != want {
			t.Errorf("%v: got %q want %q", names, got, want)
		}
	}
}
