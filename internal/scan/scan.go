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
	"regexp"
	"sort"
	"strconv"
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

type rule struct {
	id   string
	sev  Severity
	re   *regexp.Regexp
	desc string
}

// Rules run against C# source after comments are removed and \uXXXX escapes
// are decoded, so `Process.Start` is caught the same as `Process.Start`.
var codeRules = []rule{
	{"process-spawn", Critical, regexp.MustCompile(`\bProcess\s*\.\s*Start\b|\bProcessStartInfo\b|System\s*\.\s*Diagnostics\s*\.\s*Process\b`), "starts external programs"},
	{"native-interop", Critical, regexp.MustCompile(`\bDllImport\b|\bextern\s+[\w<>\[\]]+\s+\w+\s*\(|\bLibraryImport\b|\bMarshal\s*\.\s*(GetDelegateForFunctionPointer|AllocHGlobal|Copy)\b|\bunsafe\b`), "calls native code or uses unsafe memory"},
	{"network", Critical, regexp.MustCompile(`\bSystem\s*\.\s*Net\b|\bWebClient\b|\bHttpClient\b|\bWebRequest\b|\bHttpWebRequest\b|\bTcpClient\b|\bUdpClient\b|\bSocket\b|\bUnityWebRequest\b|\bWWW\s*\(`), "makes network connections"},
	{"steam-ugc", Critical, regexp.MustCompile(`\bSteamUGC\b|\bSubmitItemUpdate\b|\bSetItemContent\b|\bStartItemUpdate\b|\bUGCUpdateHandle_t\b|\bPublishedFileId_t\b|\bUgc\s*\.\s*Editor\b|\bSteamRemoteStorage\b`), "uses the Steam Workshop upload API (worm propagation vector)"},
	{"steam-friends", Critical, regexp.MustCompile(`\bSteamFriends\b|\bReplyToFriendMessage\b|\bSendClanChatMessage\b|\bSteamMatchmaking\b|\bSendLobbyChatMsg\b`), "uses Steam friends/chat API (spam vector)"},
	{"steam-auth", Critical, regexp.MustCompile(`\bGetAuthSessionTicket\b|\bGetAuthTicketForWebApi\b|\bRequestEncryptedAppTicket\b|\bGetEncryptedAppTicket\b|\bSteamUser\b`), "requests Steam auth tickets (account takeover vector)"},
	{"steamworks-any", High, regexp.MustCompile(`\bSteamworks\b|\bSteamAPI\b|\bFacepunch\s*\.\s*Steamworks\b|\bSteamClient\b`), "touches the Steamworks API"},
	{"file-delete", Critical, regexp.MustCompile(`\b(File|Directory)\s*\.\s*Delete\b|\bFileInfo\b[^;]*\.\s*Delete\b|\bDirectoryInfo\b[^;]*\.\s*Delete\b|\bFileSystem\s*\.\s*Delete`), "deletes files or folders"},
	{"registry", Critical, regexp.MustCompile(`\bMicrosoft\s*\.\s*Win32\b|\bRegistry\s*\.|\bRegistryKey\b`), "accesses the Windows registry"},
	{"dynamic-code", High, regexp.MustCompile(`\bAssembly\s*\.\s*(Load|LoadFrom|LoadFile|UnsafeLoadFrom)\b|\bSystem\s*\.\s*Reflection\s*\.\s*Emit\b|\bAppDomain\b|\bCSharpCodeProvider\b|\bCompileAssembly|\bMicrosoft\s*\.\s*CodeAnalysis\b|\bRoslyn\b`), "loads or compiles code at runtime"},
	{"reflection-by-name", High, regexp.MustCompile(`\bType\s*\.\s*GetType\s*\(|\bGetMethod\s*\(\s*"|\bInvokeMember\b|\bActivator\s*\.\s*CreateInstance\s*\(\s*(Type\s*\.\s*GetType|")`), "looks up types or methods by string name (hides intent)"},
	{"base64", High, regexp.MustCompile(`\bConvert\s*\.\s*FromBase64(String|CharArray)\b`), "decodes base64 data (common obfuscation)"},
	{"env-paths", High, regexp.MustCompile(`\bEnvironment\s*\.\s*(GetFolderPath|GetEnvironmentVariable|UserName|SpecialFolder|CurrentDirectory)\b|\bSpecialFolder\s*\.`), "reads user folders or environment variables"},
	{"file-write", Medium, regexp.MustCompile(`\bFile\s*\.\s*(WriteAll\w*|AppendAll\w*|Create|Copy|Move|Replace|Open|OpenWrite)\b|\bDirectory\s*\.\s*(Move|CreateDirectory)\b|\bStreamWriter\b|\bFileStream\b|\bBinaryWriter\b`), "writes or moves files"},
	{"file-enumerate", Medium, regexp.MustCompile(`\b(Directory|DirectoryInfo)\b[^;]*\.\s*(GetFiles|EnumerateFiles|GetDirectories|EnumerateDirectories|GetFileSystemEntries)\b`), "lists files/folders on disk"},
	{"open-url", Medium, regexp.MustCompile(`\bApplication\s*\.\s*OpenURL\b`), "opens a URL in the browser"},
	{"threads", Info, regexp.MustCompile(`\bnew\s+Thread\s*\(|\bThreadPool\b|\bTask\s*\.\s*Run\b`), "starts background threads"},
}

// Combination rules: individually harmless-ish, together they describe
// self-replication into other mods or the Workshop cache.
var (
	reCsLiteral     = regexp.MustCompile(`"[^"\n]*\.cs"`)
	reWriteAPI      = regexp.MustCompile(`\bFile\s*\.\s*(WriteAll\w*|AppendAll\w*|Copy|Create|Replace)\b|\bStreamWriter\b`)
	reGamePaths     = regexp.MustCompile(`(?i)"[^"\n]*(workshop[/\\]+content|1118200|mods[/\\]|contraptions|steamapps|people playground)[^"\n]*"`)
	reStringLit     = regexp.MustCompile(`"(?:[^"\\\n]|\\.)*"`)
	reLongB64       = regexp.MustCompile(`"[A-Za-z0-9+/]{120,}={0,2}"`)
	reUnicodeEsc    = regexp.MustCompile(`\\u([0-9A-Fa-f]{4})|\\U([0-9A-Fa-f]{8})`)
	reCharCodes     = regexp.MustCompile(`\(\s*char\s*\)\s*\d+`)
	reSuspectString = regexp.MustCompile(`(?i)^(system\.diagnostics|process|system\.net|webclient|httpclient|steamugc|steamfriends|steamuser|assembly|file|directory|registry)$`)
)

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

// Dir scans a mod folder (or a folder holding several mods).
func Dir(root string) (*Report, error) {
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
	src = string(stripBOM([]byte(src)))
	if n := len(reUnicodeEsc.FindAllStringIndex(stripStrings(stripComments(src)), -1)); n > 0 {
		r.add(High, "unicode-escapes", rel, 0, fmt.Sprintf("%d \\u escapes outside strings (identifier obfuscation)", n))
	}
	code := stripComments(decodeEscapes(src))
	// Keyword rules ignore string contents so UI text like "unsafe" does not
	// trip them; strings are checked separately below.
	lines := strings.Split(stripStrings(code), "\n")
	orig := strings.Split(code, "\n")
	seen := map[string]bool{}
	for i, ln := range lines {
		for _, ru := range codeRules {
			if seen[ru.id] {
				continue
			}
			if ru.re.MatchString(ln) {
				seen[ru.id] = true
				shown := ln
				if i < len(orig) {
					shown = orig[i]
				}
				r.add(ru.sev, ru.id, rel, i+1, ru.desc+": "+trim(shown))
			}
		}
	}

	if reCsLiteral.MatchString(code) && reWriteAPI.MatchString(code) {
		r.add(Critical, "self-replication", rel, 0, "writes files and references .cs file names (can inject code into other mods)")
	}
	if reGamePaths.MatchString(code) && (reWriteAPI.MatchString(code) || seen["file-delete"]) {
		r.add(Critical, "game-path-tamper", rel, 0, "modifies files under Mods/Contraptions/Workshop/Steam paths")
	}
	if m := reLongB64.FindString(code); m != "" {
		r.add(High, "encoded-blob", rel, 0, fmt.Sprintf("long base64-like string literal (%d chars)", len(m)-2))
	}
	if n := len(reCharCodes.FindAllString(code, -1)); n >= 8 {
		r.add(High, "char-code-strings", rel, 0, fmt.Sprintf("%d (char)NNN casts (string obfuscation)", n))
	}
	for _, lit := range reStringLit.FindAllString(code, -1) {
		s, err := strconv.Unquote(lit)
		if err != nil {
			s = strings.Trim(lit, `"`)
		}
		if reSuspectString.MatchString(strings.TrimSpace(s)) && (seen["reflection-by-name"] || seen["dynamic-code"]) {
			r.add(Critical, "reflection-sensitive-type", rel, 0, fmt.Sprintf("string %q used with reflection", s))
			break
		}
	}
	if strings.Count(code, `" + "`)+strings.Count(code, `"+"`) >= 6 && (seen["reflection-by-name"] || seen["dynamic-code"]) {
		r.add(High, "split-strings", rel, 0, "many concatenated string fragments combined with reflection")
	}
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 140 {
		s = s[:140] + "..."
	}
	return s
}

func decodeEscapes(s string) string {
	return reUnicodeEsc.ReplaceAllStringFunc(s, func(m string) string {
		v, err := strconv.ParseUint(m[2:], 16, 32)
		if err != nil {
			return m
		}
		return string(rune(v))
	})
}

// stripComments removes // and /* */ comments while leaving string and char
// literals (including verbatim @"" strings) untouched. Newlines are kept so
// line numbers stay accurate.
func stripComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				b.WriteByte('\n')
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i < len(s) && !(s[i] == '*' && i+1 < len(s) && s[i+1] == '/') {
				if s[i] == '\n' {
					b.WriteByte('\n')
				}
				i++
			}
			i++
		case c == '@' && i+1 < len(s) && s[i+1] == '"':
			j := i + 2
			for j < len(s) {
				if s[j] == '"' {
					if j+1 < len(s) && s[j+1] == '"' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, len(s))
			b.WriteString(s[i:end])
			i = end - 1
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(s) && s[j] != c && s[j] != '\n' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			end := min(j+1, len(s))
			b.WriteString(s[i:end])
			i = end - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// stripStrings blanks out string literal contents so escape counting only
// sees identifiers.
func stripStrings(s string) string {
	return reStringLit.ReplaceAllString(s, `""`)
}
