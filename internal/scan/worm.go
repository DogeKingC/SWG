package scan

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Markers of the 2026 FPS++ worms (see docs/fpsplusplus-analysis.md). They
// are checked in string literals, in what base64 literals decode to (the
// worm hid them there), and in the bytes of bundled DLLs.

type marker struct {
	s, rule, detail string
}

var wormMarkers = []marker{
	{"m_persistentcalls", "json-gadget", "a UnityEvent call list (the worm used one to call File.WriteAllBytes and its entry point)"},
	{"m_targetassemblytypename", "json-gadget", "a UnityEvent call target (the worm used one to call any method)"},
	{"binaryformatter", "deserialization", "BinaryFormatter (the worm deserialized its code loader with it)"},
	{"delegateserializationholder", "deserialization", "serialized delegates (the worm's way to get Assembly.Load without naming it)"},
	{"system.runtime.serialization.formatters", "deserialization", "a .NET object formatter"},
	{"bootstrapcontext", "deserialization", "ClaimsIdentity's bootstrap context (a deserialization trick the worm used)"},
	{"losformatter", "deserialization", "a .NET object formatter"},
	{"objectstateformatter", "deserialization", "a .NET object formatter"},
	{"netdatacontractserializer", "deserialization", "a .NET object formatter"},
	{"system.reflection.assembly", "code-loader", "the Assembly type, by name (to load code through reflection)"},
	{"assembly.load", "code-loader", "Assembly.Load, by name"},
	{"_data\\managed", "game-path-tamper", "the game's own code folder (the worm wrote its DLL there)"},
	{"_data/managed", "game-path-tamper", "the game's own code folder (the worm wrote its DLL there)"},
	{"compiledmodassemblies", "game-path-tamper", "the game's compiled-mods folder (the worm deleted and infected it)"},
	{"compiledmods", "game-path-tamper", "the game's compiled-mods folder (the worm deleted and infected it)"},
	{"rejectshadycode", "disables-protection", "the game's \"reject shady code\" protection (the worm turned it off)"},
}

// checkMarkers reports worm markers found in lowercased text.
func checkMarkers(hit func(string, Severity, int, string), line int, low, where string) {
	for _, m := range wormMarkers {
		if strings.Contains(low, m.s) {
			hit(m.rule, Critical, line, fmt.Sprintf("%s names %s", where, m.detail))
		}
	}
}

var reB64Lit = regexp.MustCompile(`^[A-Za-z0-9+/]{12,}={0,2}$`)

// checkEncoded decodes base64 literals, one by one and all of them joined
// in order (the worm split its blobs into hundreds of variables), and looks
// for a hidden executable, serialized .NET objects or worm markers.
func checkEncoded(hit func(string, Severity, int, string), lits []strLit) {
	var all strings.Builder
	first := 0
	for _, s := range lits {
		t := strings.TrimSpace(s.text)
		if !reB64Lit.MatchString(t) {
			continue
		}
		if first == 0 {
			first = s.line
		}
		if all.Len() < 64<<20 {
			all.WriteString(strings.TrimRight(t, "="))
		}
		inspectDecoded(hit, s.line, lenientB64(t))
	}
	if all.Len() > 0 {
		inspectDecoded(hit, first, lenientB64(all.String()))
	}
}

func inspectDecoded(hit func(string, Severity, int, string), line int, b []byte) {
	switch {
	case len(b) >= 2 && b[0] == 'M' && b[1] == 'Z':
		hit("embedded-executable", Critical, line, "base64 text that decodes to a Windows program or DLL (how the worm carried its payload)")
	case bytes.HasPrefix(b, []byte{0, 1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}):
		hit("deserialization", Critical, line, "base64 text that decodes to serialized .NET objects (BinaryFormatter data)")
	}
	if len(b) > 0 {
		low := strings.ToLower(string(bytes.ReplaceAll(b, []byte{0}, nil)))
		checkMarkers(hit, line, low, "encoded text")
	}
}

// lenientB64 decodes as much base64 as it can: whole 4-character groups,
// stopping at the first invalid one.
func lenientB64(s string) []byte {
	s = strings.TrimRight(s, "=")
	s = s[:len(s)/4*4]
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b
	}
	var out []byte
	for i := 0; i+4 <= len(s); i += 4 {
		b, err := base64.StdEncoding.DecodeString(s[i : i+4])
		if err != nil {
			break
		}
		out = append(out, b...)
	}
	return out
}

// dllMarkers are names a compiled worm references: spreading through the
// Workshop, disabling the game's protection, deserialization tricks, theft.
var dllMarkers = []string{"NewCommunityFile", "WhereUserPublished", "RejectShadyCode", "DelegateSerializationHolder",
	"BinaryFormatter", "m_PersistentCalls", "m_TargetAssemblyTypeName", "api.ipify.org", "STEAM_CONFIG",
	"FPSPlusPlus"}

// WormDLL reports the worm markers a compiled file references ("" if none).
func WormDLL(path string) string {
	r := &Report{}
	inspectDLL(r, "", path)
	if len(r.Findings) == 0 {
		return ""
	}
	return r.Findings[0].Detail
}

// inspectDLL looks inside a bundled DLL for worm markers (type, member and
// string names are stored as UTF-8 and UTF-16 text).
func inspectDLL(r *Report, rel, path string) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 64<<20 {
		return
	}
	var found []string
	for _, m := range dllMarkers {
		utf16 := make([]byte, 0, len(m)*2)
		for i := 0; i < len(m); i++ {
			utf16 = append(utf16, m[i], 0)
		}
		if bytes.Contains(b, []byte(m)) || bytes.Contains(b, utf16) {
			found = append(found, m)
		}
	}
	if len(found) > 0 {
		r.add(Critical, "worm-dll", rel, 0, "compiled code that references what the FPS++ worms used: "+strings.Join(found, ", "))
	}
}
