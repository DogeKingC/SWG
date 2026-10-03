package scan

import (
	"os"
	"path/filepath"
	"testing"
)

const benign = `using UnityEngine;
// Process.Start in a comment is fine
public class Mod {
    public static void Main() {
        ModAPI.Register(new Modification() {
            OriginalItem = ModAPI.FindSpawnable("Brick"),
            NameOverride = "Unsafe Brick",
            DescriptionOverride = "Totally unsafe. Uses File menu.",
            AfterSpawn = (Instance) => { Instance.GetComponent<SpriteRenderer>().color = Color.red; }
        });
    }
}`

func has(r *Report, rule string) bool {
	for _, f := range r.Findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

func TestBenignModIsClean(t *testing.T) {
	r := Source("mod.cs", benign)
	if r.Max() >= Medium {
		t.Fatalf("benign mod flagged: %+v", r.Findings)
	}
}

func TestDetections(t *testing.T) {
	cases := map[string]string{
		"process-spawn":             `System.Diagnostics.Process.Start("cmd.exe");`,
		"network":                   `var c = new System.Net.WebClient(); c.DownloadString(u);`,
		"steam-ugc":                 `var h = SteamUGC.StartItemUpdate(app, id); SteamUGC.SetItemContent(h, dir);`,
		"steam-friends":             `SteamFriends.ReplyToFriendMessage(f, msg);`,
		"file-delete":               `Directory.Delete(path, true);`,
		"native-interop":            `[DllImport("kernel32")] static extern IntPtr LoadLibrary(string s);`,
		"self-replication":          `foreach (var f in found) File.WriteAllText(Path.Combine(f, "script.cs"), payload);`,
		"game-path-tamper":          `File.Copy(me, Path.Combine(root, "workshop/content/1118200", d));`,
		"reflection-sensitive-type": `var t = Type.GetType("Process"); t.GetMethod("Start").Invoke(null, null);`,
		"encoded-blob":              `var p = "` + longB64() + `";`,
	}
	for rule, src := range cases {
		if r := Source("x.cs", src); !has(r, rule) {
			t.Errorf("%s not detected; got %+v", rule, r.Findings)
		}
	}
}

func TestUnicodeEscapeObfuscation(t *testing.T) {
	r := Source("x.cs", "System.Diagnostics.\\"+"u0050rocess.Start(\"x\");")
	if !has(r, "process-spawn") || !has(r, "unicode-escapes") {
		t.Fatalf("escaped identifier not caught: %+v", r.Findings)
	}
}

func TestStringContentsDoNotTripKeywordRules(t *testing.T) {
	r := Source("x.cs", `var s = "Process.Start and System.Net are just words here";`)
	if has(r, "process-spawn") || has(r, "network") {
		t.Fatalf("string literal tripped keyword rule: %+v", r.Findings)
	}
}

func TestDirFlagsBinariesAndManifestEscape(t *testing.T) {
	d := t.TempDir()
	must(t, os.WriteFile(filepath.Join(d, "mod.json"), []byte(`{"Name":"x","Scripts":["../../evil.cs","script.cs"]}`), 0o644))
	must(t, os.WriteFile(filepath.Join(d, "script.cs"), []byte(benign), 0o644))
	must(t, os.WriteFile(filepath.Join(d, "helper.dll"), []byte("MZ\x90\x00"), 0o644))
	must(t, os.WriteFile(filepath.Join(d, "icon.png"), []byte("MZ\x90\x00"), 0o644))
	r, err := Dir(d)
	must(t, err)
	for _, rule := range []string{"manifest-path-escape", "executable-file", "disguised-binary"} {
		if !has(r, rule) {
			t.Errorf("%s not reported: %+v", rule, r.Findings)
		}
	}
}

func longB64() string {
	b := make([]byte, 200)
	for i := range b {
		b[i] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"[i%64]
	}
	return string(b)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
