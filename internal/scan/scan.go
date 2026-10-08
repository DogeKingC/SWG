// Package scan statically inspects People Playground mod folders for code
// patterns used by malware (process spawning, networking, Steam Workshop
// uploads, friend messaging, file deletion, self-replication, obfuscation).
//
// Static scanning is a filter, not a guarantee. A determined author can hide
// behaviour from any text-based scanner. The goal is to stop the known worm
// techniques and anything that obviously does not belong in a sandbox mod.
package scan

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

type Severity int

const (
	Info Severity = iota
	Medium
	High
	Critical
)

func (s Severity) String() string {
	switch s {
	case Critical:
		return "CRITICAL"
	case High:
		return "HIGH"
	case Medium:
		return "MEDIUM"
	default:
		return "INFO"
	}
}

type Finding struct {
	Severity Severity `json:"severity"`
	Rule     string   `json:"rule"`
	File     string   `json:"file"`
	Line     int      `json:"line,omitempty"`
	Detail   string   `json:"detail"`
}

type Report struct {
	Root     string    `json:"root"`
	Files    int       `json:"files"`
	Scripts  int       `json:"scripts"`
	Findings []Finding `json:"findings"`
}

// Max returns the highest severity in the report, or -1 if there are none.
func (r *Report) Max() Severity {
	m := Severity(-1)
	for _, f := range r.Findings {
		if f.Severity > m {
			m = f.Severity
		}
	}
	return m
}

// Keys returns a set of rule+file identifiers, used to tell whether an update
// introduces findings that the installed version did not have.
func (r *Report) Keys() map[string]bool {
	k := map[string]bool{}
	for _, f := range r.Findings {
		if f.Severity >= Medium {
			k[f.Rule+"|"+filepath.ToSlash(f.File)] = true
		}
	}
	return k
}

// Files a People Playground mod has no reason to ship. The game compiles .cs
// sources itself, so a binary in a mod is either junk or a payload.
var executableExt = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bat": true, ".cmd": true,
	".ps1": true, ".psm1": true, ".vbs": true, ".vbe": true, ".js": true, ".jse": true,
	".wsf": true, ".scr": true, ".com": true, ".msi": true, ".jar": true, ".sh": true,
	".lnk": true, ".hta": true, ".pif": true, ".reg": true, ".cpl": true,
}

var nestedArchiveExt = map[string]bool{".zip": true, ".rar": true, ".7z": true, ".gz": true, ".tar": true}

// buildDirs are IDE/compiler output folders some authors ship by accident.
// The game compiles mods from source and never loads anything from them.
var buildDirs = map[string]bool{"bin": true, "obj": true, ".vs": true, ".git": true, ".idea": true}

// PruneBuildOutput deletes build output folders under root and returns the
// relative paths it removed.
func PruneBuildOutput(root string) ([]string, error) {
	var removed []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != root && buildDirs[strings.ToLower(d.Name())] {
			rel, _ := filepath.Rel(root, p)
			removed = append(removed, rel)
			if err := os.RemoveAll(p); err != nil {
				return err
			}
			return filepath.SkipDir
		}
		return nil
	})
	return removed, err
}

// Options adds what the scanner can compare against.
type Options struct {
	// GameManaged is the game's People_Playground_Data/Managed folder: a
	// bundled DLL identical to the game's own copy is not a payload.
	GameManaged string
}

// Dir scans a mod folder (or a folder holding several mods).
func Dir(root string) (*Report, error) { return DirWith(root, Options{}) }

// DirWith is Dir with options.
func DirWith(root string, opt Options) (*Report, error) {
	r := &Report{Root: root}
	var listed []string // files mod.json lists as scripts
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.Type()&fs.ModeSymlink != 0 {
			r.add(Critical, "symlink", rel, 0, "symbolic link inside mod")
			return nil
		}
		if d.IsDir() {
			return nil
		}
		r.Files++
		ext := strings.ToLower(filepath.Ext(p))
		base := strings.ToLower(d.Name())
		switch {
		case ext == ".dll":
			if lib, ok := knownLibrary(p); ok {
				r.add(lib.severity(), "known-library", rel, 0, lib.describe())
			} else if gameCopy(p, opt.GameManaged) {
				r.add(Info, "game-library", rel, 0, "identical to the game's own "+d.Name())
			} else {
				r.add(Critical, "executable-file", rel, 0, "unknown compiled library (.dll): its code cannot be checked")
				inspectDLL(r, rel, p)
			}
		case executableExt[ext]:
			r.add(Critical, "executable-file", rel, 0, "executable/script file "+ext+" has no place in a mod")
		case nestedArchiveExt[ext]:
			r.add(High, "nested-archive", rel, 0, "archive inside mod cannot be inspected")
		case ext == ".cs":
			r.Scripts++
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			scanSource(r, rel, string(b))
		case base == "mod.json":
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			listed = append(listed, checkManifest(r, filepath.Dir(p), rel, b)...)
		}
		if ext != ".cs" && ext != ".json" && !executableExt[ext] {
			if b, err := readHead(p, 4); err == nil && isNativeBinary(b) {
				r.add(Critical, "disguised-binary", rel, 0, "file is a native executable despite its extension")
			}
		}
		return nil
	})
	if err == nil {
		err = scanListed(r, root, listed)
	}
	sort.SliceStable(r.Findings, func(i, j int) bool { return r.Findings[i].Severity > r.Findings[j].Severity })
	return r, err
}

func (r *Report) add(s Severity, id, file string, line int, detail string) {
	r.Findings = append(r.Findings, Finding{Severity: s, Rule: id, File: file, Line: line, Detail: detail})
}

func readHead(p string, n int) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, n)
	k, _ := f.Read(b)
	return b[:k], nil
}

func isNativeBinary(b []byte) bool {
	if len(b) >= 2 && b[0] == 'M' && b[1] == 'Z' {
		return true
	}
	return len(b) >= 4 && b[0] == 0x7f && b[1] == 'E' && b[2] == 'L' && b[3] == 'F'
}

// checkManifest checks a mod.json's script list and returns the listed
// scripts that are not .cs files. RE_PPG refuses to compile those, but
// another loader may not, so they are scanned as code anyway.
func checkManifest(r *Report, dir, rel string, b []byte) []string {
	if _, err := manifestScripts([]byte(decodeText(b))); err != nil {
		r.add(Medium, "manifest-invalid", rel, 0, "mod.json is not strict JSON: "+err.Error())
	}
	// The game's reader is looser than JSON: check the list it would read.
	scripts, ok := ManifestScripts(b)
	if !ok {
		return nil
	}
	var other []string
	for _, s := range scripts {
		clean := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(s, `\`, "/")))
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || filepath.VolumeName(clean) != "" || strings.Contains(clean, ":") {
			r.add(Critical, "manifest-path-escape", rel, 0, fmt.Sprintf("script path %q points outside the mod", s))
			continue
		}
		if strings.ToLower(filepath.Ext(clean)) != ".cs" {
			r.add(High, "manifest-non-cs", rel, 0, fmt.Sprintf("script entry %q is not a .cs file", s))
			other = append(other, filepath.Join(dir, clean))
		}
		if _, err := os.Stat(filepath.Join(dir, clean)); err != nil {
			r.add(Info, "manifest-missing-script", rel, 0, fmt.Sprintf("script %q listed but not present", s))
		}
	}
	return other
}

// ManifestScripts reads the script list of a mod.json file's raw bytes the
// way the game does: any byte order mark, and the loose JSON (comments,
// trailing commas) its reader accepts. ok is false if no list can be read.
func ManifestScripts(raw []byte) (scripts []string, ok bool) {
	b := []byte(decodeText(raw))
	if s, err := manifestScripts(b); err == nil {
		return s, true
	}
	if s, err := manifestScripts(LooseJSON(raw)); err == nil {
		return s, true
	}
	m := reLooseScripts.FindAllSubmatch(b, -1)
	if m == nil {
		return nil, false
	}
	for _, list := range m {
		for _, q := range reJSONString.FindAll(list[1], -1) {
			if s, err := strconv.Unquote(string(q)); err == nil {
				scripts = append(scripts, s)
			}
		}
	}
	return scripts, true
}

var (
	reLooseScripts = regexp.MustCompile(`(?is)"scripts"\s*:\s*\[(.*?)\]`)
	reJSONString   = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
)

// manifestScripts returns the script list of a mod.json. JSON readers
// disagree on duplicate keys and on "Scripts" vs "scripts" (Go takes the
// last, case-insensitively), so every such key counts: otherwise a second
// list could hide the one the game reads.
func manifestScripts(b []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		if err == nil {
			err = errors.New("not a JSON object")
		}
		return nil, err
	}
	var out []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if k, _ := t.(string); strings.EqualFold(k, "Scripts") {
			var list []any
			json.Unmarshal(v, &list)
			for _, x := range list {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out, nil
}

// scanListed scans scripts named by a mod.json that the walk did not scan
// as C# (any extension but .cs), if they are files inside root.
func scanListed(r *Report, root string, listed []string) error {
	done := map[string]bool{}
	for _, p := range listed {
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || done[rel] {
			continue
		}
		done[rel] = true
		if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		r.Scripts++
		scanSource(r, rel, string(b))
	}
	return nil
}

// Source scans one C# file's text. Exposed for tests.
func Source(name, src string) *Report {
	r := &Report{Root: name, Files: 1, Scripts: 1}
	scanSource(r, name, src)
	return r
}

func scanSource(r *Report, rel, src string) {
	analyzeCSharp(r, rel, decodeText([]byte(src)))
}

// decodeText reads source the way the compiler and File.ReadAllText do:
// a byte order mark picks UTF-8, UTF-16 or UTF-32. Read as raw bytes, a
// UTF-16 file is letters separated by NULs and no rule would match it.
func decodeText(b []byte) string {
	var u16 func([]byte) uint16
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:])
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE, 0, 0}), bytes.HasPrefix(b, []byte{0, 0, 0xFE, 0xFF}):
		order := binary.ByteOrder(binary.LittleEndian)
		if b[0] == 0 {
			order = binary.BigEndian
		}
		var sb strings.Builder
		for b = b[4:]; len(b) >= 4; b = b[4:] {
			sb.WriteRune(rune(order.Uint32(b)))
		}
		return sb.String()
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		u16 = binary.LittleEndian.Uint16
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		u16 = binary.BigEndian.Uint16
	default:
		return string(b)
	}
	units := make([]uint16, 0, len(b)/2)
	for b = b[2:]; len(b) >= 2; b = b[2:] {
		units = append(units, u16(b))
	}
	return string(utf16.Decode(units))
}
