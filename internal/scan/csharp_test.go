package scan

import "testing"

// Tricks the old line-based scanner could not see.
func TestEvasionsCaught(t *testing.T) {
	cases := map[string]struct{ src, rule string }{
		"alias":         {"using P = System.Diagnostics.Process;\nclass A { void B() { P.Start(\"x\"); } }", "process-spawn"},
		"split lines":   {"class A { void B() { System.\n  Diagnostics.\n  Process.Start(\"x\"); } }", "process-spawn"},
		"comment split": {"class A { object c = new System./*hi*/Net.WebClient(); }", "network"},
		"interpolation": {"class A { string s = $\"{System.Diagnostics.Process.Start(\\\"x\\\")}\"; }", "process-spawn"},
		"using static":  {"using static System.IO.File;\nclass A { void B() { Delete(\"x\"); } }", "file-delete"},
		"global":        {"class A { void B() { global::System.IO.Directory.Delete(\"x\", true); } }", "file-delete"},
		"steam alias":   {"using U = Steamworks.SteamUGC;\nclass A { void B() { U.SubmitItemUpdate(h, \"\"); } }", "steam-ugc"},
		"namespace":     {"using System.Security.Cryptography;\nclass A {}", "namespace"},
		"unity web":     {"using UnityEngine.Networking;\nclass A { void B() { UnityWebRequest.Get(\"u\"); } }", "network"},
		"extern":        {"class A { [System.Runtime.InteropServices.DllImport(\"k\")] static extern int F(); }", "native-interop"},
		"shell string":  {"class A { string s = \"powershell -c x\"; }", "shell-string"},
		"concat refl":   {"class A { void B() { var t = System.Type.GetType(\"System.Diag\" + \"nostics.Process\"); } }", "reflection-sensitive-type"},
	}
	for name, c := range cases {
		if r := Source("x.cs", c.src); !has(r, c.rule) {
			t.Errorf("%s: %s not detected; got %+v", name, c.rule, r.Findings)
		}
	}
}

// Code-looking text in strings, comments and #if branches' strings is data.
func TestNoFalsePositives(t *testing.T) {
	cases := map[string]string{
		"raw string":      "class A { string s = \"\"\"\nSystem.Diagnostics.Process.Start\n\"\"\"; }",
		"verbatim":        "class A { string s = @\"C:\\Process.Start \"\" here\"; }",
		"comment":         "// System.Net.WebClient\n/* Process.Start */ class A {}",
		"own Process":     "namespace MyMod { class Proc { void Start() {} } class B { void C() { new Proc().Start(); } } }",
		"allowed usings":  "using System; using System.Collections.Generic; using System.Linq; using UnityEngine; using TMPro;\nclass A {}",
		"using statement": "using System;\nclass A { void B() { using (var x = new Disp()) { } using var y = new Disp(); } }",
		"char literal":    "class A { char c = '\"'; string s = \"Process\"; }",
	}
	for name, src := range cases {
		if r := Source("x.cs", src); r.Max() >= High {
			t.Errorf("%s: flagged %+v", name, r.Findings)
		}
	}
}
