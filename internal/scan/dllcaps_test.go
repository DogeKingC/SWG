package scan

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real DLLs, compiled with Roslyn from testdata/dll/*.cs: what each can do
// is read from its metadata, whatever the code looks like.
func TestAnalyzeDLL(t *testing.T) {
	for _, c := range []struct {
		dll  string
		want map[string]Severity // rule -> lowest severity expected
		none []string            // rules that must not appear
	}{
		{"clean.dll", map[string]Severity{"compiled-code": High}, []string{"process-spawn", "native-interop", "reflection-invoke", "executable-file"}},
		{"process.dll", map[string]Severity{"process-spawn": Critical, "shell-string": High}, nil},
		{"pinvoke.dll", map[string]Severity{"native-interop": Critical}, nil},
		{"hidden.dll", map[string]Severity{"reflection-invoke": High, "reflection-sensitive-type": High}, []string{"process-spawn"}},
	} {
		r := &Report{}
		analyzeDLL(r, c.dll, filepath.Join("testdata", "dll", c.dll))
		got := map[string]Severity{}
		for _, f := range r.Findings {
			if f.Severity > got[f.Rule] {
				got[f.Rule] = f.Severity
			}
		}
		for rule, sev := range c.want {
			if got[rule] < sev {
				t.Errorf("%s: %s is %v, want at least %v\n%+v", c.dll, rule, got[rule], sev, r.Findings)
			}
		}
		for _, rule := range c.none {
			if _, ok := got[rule]; ok {
				t.Errorf("%s: unexpected %s\n%+v", c.dll, rule, r.Findings)
			}
		}
		if c.dll == "clean.dll" {
			for _, f := range r.Findings {
				if f.Rule == "compiled-code" && !strings.Contains(f.Detail, "only the game, Unity and basic .NET") {
					t.Errorf("clean.dll summary: %s", f.Detail)
				}
			}
		}
	}
}

// Truncated or corrupted metadata is reported as unreadable, never a
// panic or a clean result.
func TestAnalyzeDLLMalformed(t *testing.T) {
	b, _ := os.ReadFile(filepath.Join("testdata", "dll", "process.dll"))
	for _, cut := range []int{0x40, 0x100, 0x200, len(b) - 300, len(b) - 10} {
		if _, err := ReadCLR(b[:cut]); err == nil {
			// A cut that keeps the metadata intact may still parse.
			continue
		}
	}
	// Random corruption anywhere: no panic, no hang.
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		bad := append([]byte{}, b...)
		for j := 0; j < 1+rnd.Intn(8); j++ {
			bad[rnd.Intn(len(bad))] = byte(rnd.Intn(256))
		}
		ReadCLR(bad)
	}
}

// FuzzReadCLR: any bytes, never a panic or hang (run with go test -fuzz).
func FuzzReadCLR(f *testing.F) {
	for _, n := range []string{"clean.dll", "process.dll", "pinvoke.dll", "hidden.dll"} {
		b, _ := os.ReadFile(filepath.Join("testdata", "dll", n))
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if info, err := ReadCLR(b); err == nil && info == nil {
			t.Fatal("no info and no error")
		}
	})
}

// Two type references nested in each other (a crafted file): naming them
// ends instead of recursing until the stack overflows.
func TestTypeRefNestingLoop(t *testing.T) {
	m := &metadata{strings: []byte("\x00A\x00B\x00")}
	m.rows[tTypeRef] = 2
	m.colSize[tTypeRef] = []int{2, 2, 2}
	m.rowSize[tTypeRef] = 6
	// ResolutionScope: tag 3 (TypeRef), row 2 for row 1 and row 1 for row 2.
	m.tables[tTypeRef] = []byte{2<<2 | 3, 0, 1, 0, 0, 0, 1<<2 | 3, 0, 3, 0, 0, 0}
	if n := m.typeRefName(1); !strings.HasSuffix(n, "/A") {
		t.Fatalf("got %q", n)
	}
}

// The plain-words list says what a mod can do, the same for source and
// DLLs, most serious first; nothing for a mod that only uses the game.
func TestCapabilities(t *testing.T) {
	r := &Report{}
	analyzeDLL(r, "process.dll", filepath.Join("testdata", "dll", "process.dll"))
	scanSource(r, "a.cs", `class A { void M() { System.IO.File.Delete("x"); } }`)
	got := Capabilities(r)
	if len(got) < 2 || got[0] != "start other programs" || !containsString(got, "delete files") {
		t.Fatalf("got %q", got)
	}
	clean := &Report{}
	analyzeDLL(clean, "clean.dll", filepath.Join("testdata", "dll", "clean.dll"))
	scanSource(clean, "b.cs", `class B : UnityEngine.MonoBehaviour { void Update() { transform.Rotate(0, 0, 1); } }`)
	if got := Capabilities(clean); len(got) != 0 {
		t.Fatalf("a mod using only the game and Unity: %q", got)
	}
}
