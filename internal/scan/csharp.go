package scan

import (
	"fmt"
	"regexp"
	"strings"
)

// Analysis of C# source on top of the lexer. References are resolved the
// way the compiler would see them — through `using` namespaces, `using X =
// ...` aliases, `using static` and `global::` — so renaming or splitting a
// call does not hide it. Namespaces are default-deny: any `using` of a
// framework namespace outside the allowlist is a finding, so new attack APIs
// need no new rule.

// apiRule matches fully qualified names: a pattern matches the name itself
// and anything below it ("System.Net" matches "System.Net.WebClient").
type apiRule struct {
	id       string
	sev      Severity
	patterns []string
	desc     string
}

var apiRules = []apiRule{
	{"process-spawn", Critical, []string{"System.Diagnostics.Process.Start", "System.Diagnostics.ProcessStartInfo"}, "starts external programs"},
	{"process-control", High, []string{"System.Diagnostics.Process.GetProcesses", "System.Diagnostics.Process.GetProcessesByName", "System.Diagnostics.Process.GetProcessById"}, "looks at or controls other programs"},
	{"network", Critical, []string{"System.Net", "System.Web", "UnityEngine.Networking", "UnityEngine.WWW", "UnityEngine.WWWForm"}, "makes network connections"},
	{"native-interop", Critical, []string{"System.Runtime.InteropServices.DllImport", "System.Runtime.InteropServices.DllImportAttribute",
		"System.Runtime.InteropServices.Marshal", "System.Runtime.InteropServices.NativeLibrary", "System.Runtime.InteropServices.LibraryImport",
		"System.Runtime.InteropServices.LibraryImportAttribute"}, "calls native code"},
	{"steam-ugc", Critical, []string{"Steamworks.SteamUGC", "Steamworks.SteamRemoteStorage", "Steamworks.Ugc", "Steamworks.UGCUpdateHandle_t"}, "uses the Steam Workshop upload API (worm propagation vector)"},
	{"steam-friends", Critical, []string{"Steamworks.SteamFriends", "Steamworks.SteamMatchmaking", "Steamworks.SteamNetworking", "Steamworks.SteamNetworkingSockets"}, "uses Steam friends/chat/networking (spam vector)"},
	{"steam-auth", Critical, []string{"Steamworks.SteamUser", "Steamworks.SteamEncryptedAppTicket"}, "requests Steam auth tickets (account takeover vector)"},
	{"steamworks-any", High, []string{"Steamworks", "Facepunch.Steamworks"}, "touches the Steamworks API"},
	{"file-delete", High, []string{"System.IO.File.Delete", "System.IO.Directory.Delete", "Microsoft.VisualBasic.FileIO.FileSystem.DeleteFile", "Microsoft.VisualBasic.FileIO.FileSystem.DeleteDirectory"}, "deletes files or folders"},
	{"registry", Critical, []string{"Microsoft.Win32"}, "accesses the Windows registry"},
	{"code-loader", Critical, []string{"System.Reflection.Assembly.Load", "System.Reflection.Assembly.LoadFrom", "System.Reflection.Assembly.LoadFile",
		"System.Reflection.Assembly.UnsafeLoadFrom", "System.Reflection.Assembly.LoadWithPartialName", "System.AppDomain.Load", "System.AppDomain.ExecuteAssembly",
		"System.Runtime.Loader"}, "loads compiled code at runtime (how both FPS++ worms ran their payload)"},
	{"dynamic-code", High, []string{"System.Reflection.Emit", "System.AppDomain", "System.CodeDom", "Microsoft.CSharp", "Microsoft.CodeAnalysis", "Mono.CSharp", "Mono.Cecil"}, "loads or compiles code at runtime"},
	// The FPS++++ worm never named Assembly.Load: it deserialized ready-made
	// delegates for it (BinaryFormatter, or ClaimsIdentity's bootstrap
	// context). No mod needs .NET object deserialization.
	{"deserialization", Critical, []string{"System.Runtime.Serialization.IFormatter", "System.Runtime.Serialization.Formatters",
		"System.Runtime.Serialization.SerializationInfo", "System.Runtime.Serialization.FormatterConverter", "System.Runtime.Serialization.IFormatterConverter",
		"System.Runtime.Serialization.ObjectManager", "System.Runtime.Serialization.SurrogateSelector", "System.Runtime.Serialization.ISerializationSurrogate",
		"System.Runtime.Serialization.NetDataContractSerializer", "System.Runtime.Serialization.ISerializable", "System.Security.Claims",
		"System.DelegateSerializationHolder", "System.Web.UI.LosFormatter", "System.Web.UI.ObjectStateFormatter"},
		"uses .NET object deserialization (the worm smuggled in its code loader this way)"},
	{"base64", Medium, []string{"System.Convert.FromBase64String", "System.Convert.FromBase64CharArray"}, "decodes base64 data (common obfuscation)"},
	{"env-paths", High, []string{"System.Environment.GetFolderPath", "System.Environment.GetEnvironmentVariable", "System.Environment.GetEnvironmentVariables",
		"System.Environment.UserName", "System.Environment.SpecialFolder", "System.Environment.CurrentDirectory", "System.Environment.GetCommandLineArgs"}, "reads user folders or environment variables"},
	{"file-write", Medium, []string{"System.IO.File.WriteAllText", "System.IO.File.WriteAllBytes", "System.IO.File.WriteAllLines", "System.IO.File.AppendAllText",
		"System.IO.File.AppendAllLines", "System.IO.File.Create", "System.IO.File.Copy", "System.IO.File.Move", "System.IO.File.Replace", "System.IO.File.Open",
		"System.IO.File.OpenWrite", "System.IO.StreamWriter", "System.IO.FileStream", "System.IO.BinaryWriter", "System.IO.Directory.Move", "System.IO.Directory.CreateDirectory"}, "writes or moves files"},
	{"file-enumerate", Medium, []string{"System.IO.Directory.GetFiles", "System.IO.Directory.EnumerateFiles", "System.IO.Directory.GetDirectories",
		"System.IO.Directory.EnumerateDirectories", "System.IO.Directory.GetFileSystemEntries", "System.IO.DirectoryInfo"}, "lists files/folders on disk"},
	{"open-url", Medium, []string{"UnityEngine.Application.OpenURL"}, "opens a URL in the browser"},
	{"threads", Info, []string{"System.Threading.Thread", "System.Threading.ThreadPool", "System.Threading.Tasks.Task.Run"}, "starts background threads"},
}

// Framework namespaces a mod may import without a finding (and below them).
var allowedNamespaces = []string{
	"System", "System.Collections", "System.Linq", "System.Globalization", "System.Text", "System.Threading",
	"System.Runtime.CompilerServices", "System.Runtime.ExceptionServices", "System.ComponentModel", "System.Numerics",
	"System.Diagnostics", "System.Buffers", "System.Dynamic",
	"UnityEngine", "TMPro", "Newtonsoft.Json", "Unity",
}

// Namespaces mods use legitimately but that deserve a reviewer's look.
var reviewNamespaces = map[string]string{
	"System.IO":                      "file system access",
	"System.Reflection":              "reflection",
	"System.Runtime.InteropServices": "interop attributes",
	"UnityEditor":                    "Unity editor API (does nothing in the game)",
}

// Below allowed roots but not allowed themselves (default-deny for the
// framework namespaces mods have no use for).
var deniedBelowAllowed = []string{
	"System.Net", "System.Web", "System.Security", "System.Data", "System.Xml", "System.Management", "System.ServiceProcess",
	"System.DirectoryServices", "System.Runtime.Remoting", "System.Runtime.Serialization", "System.Runtime.Loader", "System.Configuration", "System.Media",
	"System.Windows", "System.Device", "System.IO.Pipes", "System.IO.MemoryMappedFiles", "System.IO.IsolatedStorage",
	"UnityEngine.Networking", "UnityEngine.Windows", "UnityEngine.WSA", "System.Diagnostics.Eventing", "System.Threading.AccessControl",
}

var frameworkRoots = map[string]bool{"System": true, "Microsoft": true, "UnityEngine": true, "UnityEditor": true, "Mono": true,
	"Steamworks": true, "Facepunch": true, "TMPro": true, "Newtonsoft": true, "Unity": true}

// namespacePatterns name whole namespaces. When the namespace part came from
// a `using`, the code must name something inside it (Emit.OpCodes), not
// just a word equal to its last part (a method called Emit).
var namespacePatterns = map[string]bool{"System.Net": true, "System.Web": true, "UnityEngine.Networking": true, "System.Reflection.Emit": true,
	"System.CodeDom": true, "Microsoft.CSharp": true, "Microsoft.CodeAnalysis": true, "Mono.CSharp": true, "Mono.Cecil": true,
	"Microsoft.Win32": true, "Steamworks": true, "Facepunch.Steamworks": true}

// ruleIndex maps each pattern to its rules, so a name is checked by looking
// up its prefixes instead of trying every pattern.
var ruleIndex = func() map[string][]*apiRule {
	m := map[string][]*apiRule{}
	for i := range apiRules {
		for _, p := range apiRules[i].patterns {
			m[p] = append(m[p], &apiRules[i])
		}
	}
	return m
}()

// matchRules calls f for every rule pattern that is a prefix of name.
func matchRules(name string, f func(p string, ru *apiRule)) {
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			p := name[:i]
			for _, ru := range ruleIndex[p] {
				f(p, ru)
			}
		}
	}
}

func under(name, pattern string) bool {
	return name == pattern || strings.HasPrefix(name, pattern+".")
}

// namespaceVerdict classifies a namespace used in a `using` directive.
func namespaceVerdict(ns string) (Severity, string, bool) {
	root := ns
	if i := strings.IndexByte(ns, '.'); i >= 0 {
		root = ns[:i]
	}
	if !frameworkRoots[root] {
		return 0, "", false // the mod's own or the game's namespaces
	}
	for _, d := range deniedBelowAllowed {
		if under(ns, d) {
			return High, "namespace " + ns + " is not on the allowlist", true
		}
	}
	for p, why := range reviewNamespaces {
		if under(ns, p) {
			return Medium, "uses " + p + " (" + why + ")", true
		}
	}
	for _, a := range allowedNamespaces {
		if under(ns, a) {
			return 0, "", false
		}
	}
	return High, "namespace " + ns + " is not on the allowlist", true
}

var (
	reLongB64Str  = regexp.MustCompile(`^[A-Za-z0-9+/]{120,}={0,2}$`)
	reGamePathStr = regexp.MustCompile(`(?i)(workshop[/\\]+content|1118200|(^|[/\\])mods([/\\]|$)|contraptions|steamapps|people playground)`)
	reShellStr    = regexp.MustCompile(`(?i)\b(cmd(\.exe)?|powershell|pwsh|/bin/(ba)?sh|bash|wscript|cscript|rundll32|regsvr32|mshta|curl|wget)\b`)
	// Names that make a reflection lookup dangerous. File and folder access
	// through reflection gets around the game's block on System.IO (HIGH);
	// loading code, processes, network, Steam or the registry is CRITICAL.
	sensitiveExact = map[string]Severity{"process": Critical, "assembly": Critical, "registry": Critical, "registrykey": Critical,
		"webclient": Critical, "httpclient": Critical, "socket": Critical, "tcpclient": Critical, "steamugc": Critical, "steamfriends": Critical,
		"steamuser": Critical, "marshal": Critical, "appdomain": Critical, "loadfrom": Critical, "loadfile": Critical,
		"file": High, "directory": High, "environment": High, "readallbytes": High, "writeallbytes": High, "writealltext": High}
	sensitiveSubstr = []struct {
		s   string
		sev Severity
	}{{"system.diagnostics.process", Critical}, {"processstartinfo", Critical}, {"system.net", Critical}, {"webclient", Critical},
		{"httpclient", Critical}, {"steamugc", Critical}, {"steamfriends", Critical}, {"steamuser", Critical}, {"steamworks", Critical},
		{"microsoft.win32", Critical}, {"system.reflection.emit", Critical}, {"system.reflection.assembly", Critical}, {"assembly.load", Critical},
		{"system.runtime.interopservices", Critical}, {"dllimport", Critical}, {"kernel32", Critical}, {"user32", Critical},
		{"system.io.file", High}, {"system.io.directory", High}}
)

// lastSegment turns "System.Reflection.Assembly, mscorlib" into "assembly":
// reflection lookups name types by their full, assembly-qualified name.
func lastSegment(s string) string {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

type ref struct {
	parts []string
	line  int
}

// analyzeCSharp runs the token-based rules over one file.
func analyzeCSharp(r *Report, rel, src string) {
	lx := lexCSharp(src)
	toks := lx.toks
	if lx.unicodeEscapes > 0 {
		r.add(High, "unicode-escapes", rel, 0, fmt.Sprintf("%d \\u escapes in identifiers (identifier obfuscation)", lx.unicodeEscapes))
	}

	// using directives, aliases, using static, namespace declarations
	imports := map[string]bool{}
	aliases := map[string]string{}
	var statics []string
	isDirective := map[int]bool{}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind != tIdent {
			continue
		}
		if t.text == "namespace" {
			if parts, _ := chainAt(toks, i+1); len(parts) > 0 {
				for k := 1; k <= len(parts); k++ {
					imports[strings.Join(parts[:k], ".")] = true
				}
			}
			continue
		}
		if t.text != "using" {
			continue
		}
		j := i + 1
		static := false
		if j < len(toks) && toks[j].kind == tIdent && toks[j].text == "static" {
			static = true
			j++
		}
		alias := ""
		if j+1 < len(toks) && toks[j].kind == tIdent && toks[j+1].text == "=" {
			alias = toks[j].text
			j += 2
		}
		parts, end := chainAt(toks, j)
		// Skip generic args in alias targets: using L = List<int>;
		for end < len(toks) && toks[end].text != ";" && toks[end].text != "(" && end-j < 64 {
			end++
		}
		if len(parts) == 0 || end >= len(toks) || toks[end].text != ";" {
			continue // a using statement (using (x) / using var x = ...), not a directive
		}
		name := strings.Join(trimGlobal(parts), ".")
		for k := i; k <= end; k++ {
			isDirective[k] = true
		}
		switch {
		case alias != "":
			aliases[alias] = name
		case static:
			statics = append(statics, name)
		default:
			imports[name] = true
		}
		if sev, why, ok := namespaceVerdict(name); ok {
			r.add(sev, "namespace", rel, t.line, why)
		}
		for _, ru := range apiRules {
			for _, p := range ru.patterns {
				if under(name, p) && ru.sev >= High {
					r.add(ru.sev, ru.id, rel, t.line, ru.desc+": using "+name)
				}
			}
		}
	}

	// References: every identifier chain, resolved through usings.
	seen := map[string]bool{}
	hit := func(id string, sev Severity, line int, detail string) {
		if !seen[id] {
			seen[id] = true
			r.add(sev, id, rel, line, detail)
		}
	}
	var refs []ref
	var refNew []bool
	for i := 0; i < len(toks); i++ {
		if toks[i].kind != tIdent || isDirective[i] {
			continue
		}
		if i > 0 && (toks[i-1].text == "." || toks[i-1].text == "::") {
			continue // inside a chain started earlier
		}
		parts, end := chainAt(toks, i)
		refs = append(refs, ref{trimGlobal(parts), toks[i].line})
		refNew = append(refNew, i > 0 && toks[i-1].kind == tIdent && toks[i-1].text == "new")
		i = end - 1
	}
	for i, rf := range refs {
		for _, rs := range resolve(rf.parts, imports, aliases, statics) {
			matchRules(rs.name, func(p string, ru *apiRule) {
				// The pattern must reach into what the code wrote, not only
				// into the namespace a `using` supplied.
				if len(p) <= rs.prefix || rs.prefix > 0 && namespacePatterns[p] && len(rs.name) == len(p) {
					return
				}
				hit(ru.id, ru.sev, rf.line, ru.desc+": "+strings.Join(rf.parts, "."))
			})
			if rs.prefix == 0 {
				for _, d := range deniedBelowAllowed {
					if under(rs.name, d) {
						hit("namespace", High, rf.line, "uses "+d+", which is not on the allowlist")
					}
				}
			}
			// new System.Diagnostics.Process() — a process object.
			if rs.name == "System.Diagnostics.Process" && rs.prefix < len("System.Diagnostics.Process") && refNew[i] {
				hit("process-spawn", Critical, rf.line, "creates a process object: "+strings.Join(rf.parts, "."))
			}
		}
	}

	// Keywords and patterns
	reflectionByName := false
	charCasts, splitConcats := 0, 0
	for i, t := range toks {
		if t.kind == tIdent {
			switch t.text {
			case "extern":
				hit("native-interop", Critical, t.line, "declares an extern (native) method")
			case "unsafe":
				hit("unsafe-code", High, t.line, "uses unsafe code (raw memory access)")
			case "delegate":
				if i+1 < len(toks) && toks[i+1].text == "*" {
					hit("native-interop", Critical, t.line, "uses function pointers")
				}
			case "GetType", "GetMethod", "GetField", "GetProperty", "GetMember", "InvokeMember", "CreateInstance", "GetTypeFromProgID", "GetTypeFromCLSID":
				if i > 0 && toks[i-1].text == "." && i+2 < len(toks) && toks[i+1].text == "(" && (toks[i+2].kind == tString || toks[i+2].kind == tIdent && t.text != "GetType") {
					if toks[i+2].kind == tString || t.text == "InvokeMember" || t.text == "CreateInstance" {
						reflectionByName = true
						hit("reflection-by-name", Medium, t.line, "looks up types or members by name: "+t.text+"(...)")
					}
				}
			case "char":
				if i > 0 && toks[i-1].text == "(" && i+2 < len(toks) && toks[i+1].text == ")" && toks[i+2].kind == tNumber {
					charCasts++
				}
			case "DefaultMembersSearchFlags":
				hit("json-gadget", Critical, t.line, "makes the JSON reader fill private fields (the worm built UnityEvents that call any method this way)")
			case "TypeNameHandling":
				hit("json-gadget", Critical, t.line, "lets JSON data choose which .NET types to create")
			case "RejectShadyCode":
				hit("disables-protection", Critical, t.line, "touches the game's \"reject shady code\" protection (the worm turned it off)")
			case "DeserializeObject", "Deserialize", "PopulateObject":
				// Deserializing into a UnityEvent, or into a type named by a
				// string, makes data decide what code runs.
				for k := i + 1; k < len(toks) && k < i+40 && toks[k].text != ";"; k++ {
					if toks[k].kind == tIdent && strings.HasPrefix(toks[k].text, "UnityEvent") {
						hit("json-gadget", Critical, t.line, "deserializes a UnityEvent from data (makes it call any method, e.g. File.WriteAllBytes)")
						break
					}
					if toks[k].kind == tIdent && toks[k].text == "GetType" {
						hit("json-gadget", Critical, t.line, "deserializes into a type named by a string")
						break
					}
				}
			}
		}
		if t.kind == tString && i+2 < len(toks) && toks[i+1].text == "+" && toks[i+2].kind == tString {
			splitConcats++
		}
	}
	if charCasts >= 8 {
		hit("char-code-strings", High, 0, fmt.Sprintf("%d (char)NNN casts (string obfuscation)", charCasts))
	}

	// String literals, with adjacent "a" + "b" concatenations joined.
	writes := seen["file-write"] || seen["file-delete"]
	destructive := seen["file-delete"] || seen["file-enumerate"]
	dynamic := reflectionByName || seen["dynamic-code"] || seen["code-loader"]
	for _, s := range joinedStrings(toks) {
		low := strings.ToLower(strings.Join(strings.Fields(s.text), ""))
		sev, ok := sensitiveExact[low]
		if !ok {
			sev, ok = sensitiveExact[lastSegment(low)]
		}
		bySubstr := false
		for _, w := range sensitiveSubstr {
			if strings.Contains(low, w.s) && (!ok || w.sev > sev) {
				sev, ok, bySubstr = w.sev, true, true
			}
		}
		switch {
		case ok && dynamic && sev >= Critical:
			hit("reflection-sensitive-type", sev, s.line, fmt.Sprintf("string %q is used with reflection", clip(s.text)))
		case ok && dynamic:
			hit("reflection-file-access", sev, s.line, fmt.Sprintf("string %q is used with reflection (gets around the game's block on file access)", clip(s.text)))
		case bySubstr:
			hit("sensitive-string", Medium, s.line, fmt.Sprintf("string mentions %q", clip(s.text)))
		}
		checkMarkers(hit, s.line, low, "string")
		if reShellStr.MatchString(s.text) {
			hit("shell-string", High, s.line, fmt.Sprintf("string names a shell or download tool: %q", clip(s.text)))
		}
		if reLongB64Str.MatchString(s.text) {
			if seen["dynamic-code"] || seen["code-loader"] {
				hit("encoded-code", Critical, s.line, "embeds encoded data in a file that loads code at runtime")
			} else {
				hit("encoded-blob", Medium, s.line, fmt.Sprintf("long base64-like string literal (%d chars), e.g. an embedded image", len(s.text)))
			}
		}
		lowText := strings.ToLower(s.text)
		gamePath := reGamePathStr.MatchString(s.text)
		if writes && (strings.HasSuffix(lowText, ".cs") || strings.Contains(lowText, "mod.json")) {
			// Writing scripts is what the worm did to other mods; doing it
			// while listing folders or naming game paths is the full pattern.
			if destructive || gamePath {
				hit("self-replication", Critical, s.line, "writes .cs / mod.json files while listing folders or naming game paths (can inject code into other mods)")
			} else {
				hit("writes-scripts", High, s.line, "writes .cs / mod.json files")
			}
		}
		if writes && gamePath {
			if destructive {
				hit("game-path-tamper", Critical, s.line, fmt.Sprintf("deletes or rewrites files under game/Steam paths (%q)", clip(s.text)))
			} else {
				hit("game-path-write", High, s.line, fmt.Sprintf("writes files under game/Steam paths (%q)", clip(s.text)))
			}
		}
	}
	checkEncoded(hit, joinedStrings(toks))
	if seen["file-delete"] && seen["file-enumerate"] {
		hit("mass-delete", Critical, 0, "lists folders and deletes files (the worm deleted game files this way)")
	}
	if seen["base64"] && (seen["dynamic-code"] || seen["code-loader"]) {
		hit("encoded-code", Critical, 0, "decodes base64 and loads code at runtime")
	}
	if splitConcats >= 6 && dynamic {
		hit("split-strings", High, 0, "many concatenated string fragments combined with reflection")
	}
}

func clip(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

func trimGlobal(parts []string) []string {
	if len(parts) > 1 && parts[0] == "global" {
		return parts[1:]
	}
	return parts
}

// chainAt reads Ident ( ("." | "::") Ident )* starting at i and returns the
// parts and the index after the chain.
func chainAt(toks []token, i int) ([]string, int) {
	var parts []string
	for i < len(toks) && toks[i].kind == tIdent {
		parts = append(parts, toks[i].text)
		if i+2 < len(toks) && (toks[i+1].text == "." || toks[i+1].text == "::") && toks[i+2].kind == tIdent {
			i += 2
			continue
		}
		i++
		break
	}
	return parts, i
}

// implicitImports are assumed in scope for the dangerous-API rules even
// without a `using`, so `Process.Start` or `SteamUGC.X` is caught however
// the file is set up. This errs on the side of flagging.
var implicitImports = []string{"System", "System.IO", "System.Diagnostics", "System.Reflection", "System.Net", "System.Net.Http",
	"System.Runtime.InteropServices", "Steamworks", "UnityEngine", "UnityEngine.Networking", "Microsoft.Win32"}

// resolved is a fully qualified name a reference can denote; prefix is how
// many characters of it come from a `using` namespace rather than from the
// code itself.
type resolved struct {
	name   string
	prefix int
}

// resolve lists the fully qualified names a reference can denote.
func resolve(parts []string, imports map[string]bool, aliases map[string]string, statics []string) []resolved {
	if len(parts) == 0 {
		return nil
	}
	full := strings.Join(parts, ".")
	out := []resolved{{full, 0}}
	if a, ok := aliases[parts[0]]; ok {
		out = append(out, resolved{strings.Join(append([]string{a}, parts[1:]...), "."), 0})
	}
	add := func(ns string) { out = append(out, resolved{ns + "." + full, len(ns)}) }
	for ns := range imports {
		add(ns)
	}
	for _, st := range statics {
		add(st)
	}
	for _, ns := range implicitImports {
		if !imports[ns] {
			add(ns)
		}
	}
	return out
}

type strLit struct {
	text string
	line int
}

// joinedStrings returns string literals, joining "a" + "b" + ... chains.
func joinedStrings(toks []token) []strLit {
	var out []strLit
	for i := 0; i < len(toks); i++ {
		if toks[i].kind != tString {
			continue
		}
		s := strLit{toks[i].text, toks[i].line}
		for i+2 < len(toks) && toks[i+1].text == "+" && toks[i+2].kind == tString {
			s.text += toks[i+2].text
			i += 2
		}
		out = append(out, s)
	}
	return out
}
