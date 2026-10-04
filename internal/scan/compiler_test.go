package scan

import (
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// Each source below compiles with Roslyn and starts a process (checked with
// the .NET 8 compiler; RE_PPG compiles mods with Roslyn and reads sources
// with File.ReadAllText). The scanner must read it the way the compiler does.
func TestCompilerReadsLikeTheCompiler(t *testing.T) {
	call := func(name string) string {
		return `  System.Diagnostics.` + name + `.Start("/bin/echo", "x").WaitForExit();` + "\n"
	}
	cases := map[string]string{
		"CR ends a comment":        "  // comment\r" + call("Process"),
		"U+2028 ends a comment":    "  // comment " + call("Process"),
		"U+2029 ends a comment":    "  // comment " + call("Process"),
		"NEL ends a directive":     "#region x\u0085" + call("Process") + "#endregion\n",
		"soft hyphen in a name":    call("Pro­cess"),
		"zero-width in a name":     call("​Process"),
		"escaped format character": call(`Pro­cess`),
	}
	for name, body := range cases {
		src := "class P { static void Main() {\n" + body + "}}\n"
		if r := Source("x.cs", src); !has(r, "process-spawn") {
			t.Errorf("%s: process start not detected; got %+v", name, r.Findings)
		}
	}
	if r := Source("x.cs", "class P { void M() { System.Diagnostics.Pro­cess.Start(\"a\"); } }"); !has(r, "hidden-characters") {
		t.Errorf("hidden characters not reported: %+v", r.Findings)
	}
}

func TestUTF16AndUTF32Sources(t *testing.T) {
	src := `class P { static void Main() { System.Diagnostics.Process.Start("/bin/echo", "x"); } }`
	u := utf16.Encode([]rune(src))
	le, be := []byte{0xFF, 0xFE}, []byte{0xFE, 0xFF}
	for _, c := range u {
		le = append(le, byte(c), byte(c>>8))
		be = append(be, byte(c>>8), byte(c))
	}
	u32 := []byte{0xFF, 0xFE, 0, 0}
	for _, c := range src {
		u32 = append(u32, byte(c), byte(c>>8), byte(c>>16), byte(c>>24))
	}
	for name, b := range map[string][]byte{"UTF-16LE": le, "UTF-16BE": be, "UTF-32LE": u32} {
		if r := Source("x.cs", string(b)); !has(r, "process-spawn") {
			t.Errorf("%s source: process start not detected; got %+v", name, r.Findings)
		}
	}
}

func TestReflectionWithComputedTypeName(t *testing.T) {
	src := `class P { static void Main() {
  string ns = "System.Diagnostics.", n = "Pro", asm = ", System.Diagnostics.";
  var t = System.Type.GetType(ns + n + "cess" + asm + n + "cess");
  t.GetMethod("Start", new[] { typeof(string), typeof(string) }).Invoke(null, new object[] { "/bin/echo", "x" });
}}`
	if r := Source("x.cs", src); r.Max() < High || !has(r, "reflection-computed-type") {
		t.Errorf("type name built at runtime not flagged HIGH: %+v", r.Findings)
	}
	// Literal names are judged by the string rules, not this one.
	for _, ok := range []string{`var t = Type.GetType("MyMod.Thing");`, `var t = obj.GetType();`, `var t = asm.GetType("MyMod.Thing", true);`} {
		if r := Source("x.cs", ok); has(r, "reflection-computed-type") {
			t.Errorf("%s flagged: %+v", ok, r.Findings)
		}
	}
}

// A file mod.json lists is scanned as code, whatever its extension (RE_PPG
// only compiles .cs files; other loaders may not check).
func TestListedNonCSScriptsAreScanned(t *testing.T) {
	payload := `class P { void M() { System.Diagnostics.Process.Start("calc"); } }`
	for name, manifest := range map[string]string{
		"txt script":         `{"Name":"m","Scripts":["main.txt"]}`,
		"backslash path":     `{"Name":"m","Scripts":["sub\\main.txt"]}`,
		"lowercase key last": `{"Name":"m","Scripts":["main.txt"],"scripts":["ok.cs"]}`,
		"duplicate key":      `{"Name":"m","Scripts":["main.txt"],"Scripts":["ok.cs"]}`,
	} {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
		os.WriteFile(filepath.Join(dir, "mod.json"), []byte(manifest), 0o644)
		os.WriteFile(filepath.Join(dir, "ok.cs"), []byte(benign), 0o644)
		os.WriteFile(filepath.Join(dir, "main.txt"), []byte(payload), 0o644)
		os.WriteFile(filepath.Join(dir, "sub", "main.txt"), []byte(payload), 0o644)
		r, err := Dir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !has(r, "process-spawn") || !has(r, "manifest-non-cs") {
			t.Errorf("%s: listed script not scanned: %+v", name, r.Findings)
		}
	}
}

// A lone formatting character must not make the lexer skip the next one.
func TestLoneFormatCharacterKeepsLexerInSync(t *testing.T) {
	src := "class P { void M() { var s = \u200b\"x\"; System.Diagnostics.Process.Start(\"calc\"); var u = \"y\"; } }"
	if r := Source("x.cs", src); !has(r, "process-spawn") {
		t.Errorf("process start after a lone format character not detected: %+v", r.Findings)
	}
}
