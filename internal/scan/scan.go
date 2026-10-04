// Package scan statically inspects People Playground mod folders for code
// patterns used by malware (process spawning, networking, Steam Workshop
// uploads, friend messaging, file deletion, self-replication, obfuscation).
//
// Static scanning is a filter, not a guarantee. A determined author can hide
// behaviour from any text-based scanner. The goal is to stop the known worm
// techniques and anything that obviously does not belong in a sandbox mod.
package scan

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
			checkManifest(r, filepath.Dir(p), rel, b)
		}
		if ext != ".cs" && ext != ".json" && !executableExt[ext] {
			if b, err := readHead(p, 4); err == nil && isNativeBinary(b) {
				r.add(Critical, "disguised-binary", rel, 0, "file is a native executable despite its extension")
			}
		}
		return nil
	})
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

type manifest struct {
	Name       string   `json:"Name"`
	EntryPoint string   `json:"EntryPoint"`
	Scripts    []string `json:"Scripts"`
}

func checkManifest(r *Report, dir, rel string, b []byte) {
	var m manifest
	if err := json.Unmarshal(stripBOM(b), &m); err != nil {
		r.add(Medium, "manifest-invalid", rel, 0, "mod.json does not parse: "+err.Error())
		return
	}
	for _, s := range m.Scripts {
		clean := filepath.Clean(filepath.FromSlash(s))
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			r.add(Critical, "manifest-path-escape", rel, 0, fmt.Sprintf("script path %q points outside the mod", s))
			continue
		}
		if strings.ToLower(filepath.Ext(clean)) != ".cs" {
			r.add(High, "manifest-non-cs", rel, 0, fmt.Sprintf("script entry %q is not a .cs file", s))
		}
		if _, err := os.Stat(filepath.Join(dir, clean)); err != nil {
			r.add(Info, "manifest-missing-script", rel, 0, fmt.Sprintf("script %q listed but not present", s))
		}
	}
}

func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// Source scans one C# file's text. Exposed for tests.
func Source(name, src string) *Report {
	r := &Report{Root: name, Files: 1, Scripts: 1}
	scanSource(r, name, src)
	return r
}

func scanSource(r *Report, rel, src string) {
	analyzeCSharp(r, rel, string(stripBOM([]byte(src))))
}
