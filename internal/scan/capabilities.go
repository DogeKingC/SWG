package scan

import "sort"

// What a mod can do, in plain words, from its findings: the same rules
// apply to C# source and to compiled DLLs, so this reads the same for both.
var capabilityWords = map[string]string{
	"process-spawn":             "start other programs",
	"process-control":           "look at or control other programs",
	"network":                   "use the internet",
	"native-interop":            "run native code the scanner can't read",
	"native-code":               "run native code the scanner can't read",
	"executable-file":           "contain code the scanner can't read",
	"disguised-binary":          "contain a program disguised as another file",
	"code-loader":               "load other compiled code while running",
	"dynamic-code":              "load or compile other code while running",
	"reflection-invoke":         "reach code by name (reflection), beyond what can be listed",
	"reflection-by-name":        "reach code by name (reflection), beyond what can be listed",
	"reflection-sensitive-type": "reach code by name (reflection), beyond what can be listed",
	"reflection-computed-type":  "reach code by name (reflection), beyond what can be listed",
	"reflection-enumerate":      "reach code by name (reflection), beyond what can be listed",
	"reflection-file-access":    "read or write files through reflection",
	"file-delete":               "delete files",
	"file-write":                "write or move files",
	"file-enumerate":            "list files and folders",
	"env-paths":                 "read your user folders or environment",
	"registry":                  "change the Windows registry",
	"steam-ugc":                 "use your Steam account",
	"steam-friends":             "use your Steam account",
	"steam-auth":                "use your Steam account",
	"steamworks-any":            "use your Steam account",
	"deserialization":           "turn data into running objects (.NET deserialization)",
	"open-url":                  "open web pages",
	"self-replication":          "do what the FPS++ worms did",
	"game-path-tamper":          "do what the FPS++ worms did",
	"mass-delete":               "do what the FPS++ worms did",
	"worm-dll":                  "do what the FPS++ worms did",
	"blocklisted":               "is on the blocklist",
}

// Capabilities lists what the scanned mod can do, most serious first
// (Medium findings and up); nil when nothing beyond the game, Unity and
// basic C#.
func Capabilities(r *Report) []string {
	best := map[string]Severity{}
	for _, f := range r.Findings {
		w, ok := capabilityWords[f.Rule]
		if !ok || f.Severity < Medium {
			continue
		}
		if s, seen := best[w]; !seen || f.Severity > s {
			best[w] = f.Severity
		}
	}
	out := make([]string, 0, len(best))
	for w := range best {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if best[out[i]] != best[out[j]] {
			return best[out[i]] > best[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
