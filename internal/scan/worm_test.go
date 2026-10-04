package scan

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// Neutralized copies of the 2026 FPS++ worms' loader templates (see
// docs/fpsplusplus-analysis.md): the same structure, fake payloads.
func TestFPSPlusPlusVariants(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	cases := map[string]struct {
		src   string
		rules []string
	}{
		// FPS+++++ variant A: BinaryFormatter made through Json.NET, then
		// deserialized delegates for Assembly.Load and Type.InvokeMember.
		"binaryformatter": {`using System; using System.IO; using System.Runtime.Serialization; using Newtonsoft.Json;
namespace Xq { class Kw { public static void OnLoad() {
IFormatter bf = (IFormatter)JsonConvert.DeserializeObject("{}", Type.GetType("System.Runtime.Serialization.Formatters.Binary.BinaryFormatter"));
string asm_1 = "TVqQAAMAAAAEAAAA";
string ms_1 = "AAEAAAD/////AQAAAAAAAAAE";
MemoryStream ms = new MemoryStream(Convert.FromBase64String(ms_1));
var vt = ((Func<byte[], object>, Func<Type, string, int, System.Reflection.Binder, object, object[], object>))bf.Deserialize(ms);
} public static void Main() {} } }`, []string{"deserialization", "json-gadget", "embedded-executable"}},
		// Variant B: a UnityEvent deserialized from JSON calls File.WriteAllBytes.
		"unityevent": {`using System; using System.Text; using System.Reflection; using UnityEngine.Events; using Newtonsoft.Json; using Newtonsoft.Json.Serialization;
class Kw { public static void OnLoad() {
var w = JsonConvert.DeserializeObject<UnityEvent<string, byte[]>>(Encoding.UTF8.GetString(Convert.FromBase64String("` + b64(`{"m_PersistentCalls":{"m_Calls":[{"m_TargetAssemblyTypeName":"System.IO.File, mscorlib","m_MethodName":"WriteAllBytes"}]}}`) + `")),
  new JsonSerializerSettings() { ContractResolver = new DefaultContractResolver() { DefaultMembersSearchFlags = BindingFlags.Instance | BindingFlags.NonPublic } });
w.Invoke("People Playground_Data\\Managed/Xq.dll", new byte[0]);
} }`, []string{"json-gadget", "game-path-tamper"}},
		// The marker hidden only in base64, split across variables.
		"split base64": {`class Kw { void F() { string a = "` + b64(`{"m_PersistentCalls":{"m_Calls":[{"m_Tar`)[:24] + `"; string b = "` + b64(`{"m_PersistentCalls":{"m_Calls":[{"m_Tar`)[24:] + `"; } }`, []string{"json-gadget"}},
		// Variant C: ClaimsIdentity's bootstrap context deserializes the delegates.
		"claimsidentity": {`using System; using System.Runtime.Serialization;
public class CustomClaimsIdentity : System.Security.Claims.ClaimsIdentity { public CustomClaimsIdentity(SerializationInfo info) : base(info) {} }
class Kw { public static void OnLoad() {
SerializationInfo si = new(typeof(int), new FormatterConverter());
si.AddValue("System.Security.ClaimsIdentity.bootstrapContext", "AAEAAAD/////");
} }`, []string{"deserialization"}},
		// FPS++ (February): load a bundled DLL through reflection.
		"reflective load": {`namespace Mod { class Mod { public static void OnLoad() {
System.Type t2 = System.Type.GetType("System.Reflection.Assembly, mscorlib");
object asm = t2.InvokeMember("Load", System.Reflection.BindingFlags.InvokeMethod, null, null, new object[] { new byte[0] });
} } }`, []string{"code-loader"}},
		"protection off": {`class A { void B() { UserPreferenceManager.Current.RejectShadyCode = false; } }`, []string{"disables-protection"}},
	}
	for name, c := range cases {
		r := Source("script.cs", c.src)
		for _, rule := range c.rules {
			found := false
			for _, f := range r.Findings {
				if f.Rule == rule && f.Severity == Critical {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: no CRITICAL %s; got %+v", name, rule, r.Findings)
			}
		}
	}
}

// Ordinary JSON, images and base64 must stay quiet.
func TestWormRulesNoFalsePositives(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nsome image bytes here"))
	cases := map[string]string{
		"json settings": `using Newtonsoft.Json; class S { public int A; } class B { void C() { var s = JsonConvert.DeserializeObject<S>("{\"A\":1}"); } }`,
		"base64 image":  `class B { string img = "` + png + `"; }`,
		"mods folder":   `class B { string s = "My mod folder"; }`,
	}
	for name, src := range cases {
		r := Source("x.cs", src)
		for _, f := range r.Findings {
			if f.Severity >= High {
				t.Errorf("%s: flagged %+v", name, f)
			}
		}
	}
}

func TestWormDLL(t *testing.T) {
	dir := t.TempDir()
	// A fake "DLL" holding the names the FPS++ payload references, as
	// UTF-8 (metadata) and UTF-16 (user strings).
	utf16 := func(s string) []byte {
		var b []byte
		for i := 0; i < len(s); i++ {
			b = append(b, s[i], 0)
		}
		return b
	}
	data := append([]byte("MZ\x90\x00 ... get_NewCommunityFile ... "), utf16("STEAM_CONFIG")...)
	os.WriteFile(filepath.Join(dir, "x.dll"), data, 0o644)
	os.WriteFile(filepath.Join(dir, "clean.dll"), []byte("MZ\x90\x00 nothing to see"), 0o644)
	r, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range r.Findings {
		if f.Rule == "worm-dll" {
			n++
			if f.File != "x.dll" {
				t.Errorf("worm-dll on %s", f.File)
			}
		}
	}
	if n != 1 {
		t.Errorf("want one worm-dll finding, got %+v", r.Findings)
	}
}
