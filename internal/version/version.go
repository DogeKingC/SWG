// Package version compares mod version strings.
package version

import (
	"regexp"
)

// Compare compares mod.json ModVersion strings numerically
// ("4.0" > "3.2", "1.75.2" > "1.70.8"). Unknown versions sort lowest.
func Compare(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	if pa == nil || pb == nil {
		switch {
		case pa == nil && pb == nil:
			return 0
		case pa == nil:
			return -1
		default:
			return 1
		}
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

var reDigits = regexp.MustCompile(`\d+`)

func versionParts(v string) []int {
	var out []int
	for _, d := range reDigits.FindAllString(v, 6) {
		n := 0
		for _, c := range d {
			n = n*10 + int(c-'0')
			if n > 1e8 {
				break
			}
		}
		out = append(out, n)
	}
	return out
}
