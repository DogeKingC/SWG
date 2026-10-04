package app

import "testing"

func TestAllowedURL(t *testing.T) {
	ok := []string{"https://gamebanana.com/mods/1", "https://modsbase.com/abc/x.zip.html", "https://www.top-mods.com/x",
		"https://github.com/Trlydev/SWG", "http://127.0.0.1:4455/#tok"}
	bad := []string{"file:///C:/Windows/System32/calc.exe", "http://gamebanana.com/", "https://gamebanana.com.evil.example/",
		"https://evil.example/?https://gamebanana.com/", "steam://run/1", "https://user@gamebanana.com/", "javascript:alert(1)",
		"https://gamebanana.com:8443/", `C:\Windows\notepad.exe`, "ms-settings:"}
	for _, u := range ok {
		if !AllowedURL(u) {
			t.Errorf("refused %s", u)
		}
	}
	for _, u := range bad {
		if AllowedURL(u) {
			t.Errorf("allowed %s", u)
		}
	}
}

func TestCacheDirRejectsTraversal(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	for _, k := range []string{"../../x", "sky:../../x", `gb:..\..\x`, "..", "a/b", ""} {
		if _, err := CacheDir(k); err == nil {
			t.Errorf("accepted %q", k)
		}
	}
	if _, err := CacheDir("sky:3801154351"); err != nil {
		t.Fatal(err)
	}
}

func TestYMD(t *testing.T) {
	for in, want := range map[string]string{
		"19.09.2026": "2026-09-19", "2026-09-27 15:31:50": "2026-09-27", "2026-10-03T16:48:28Z": "2026-10-03",
		"2026-10-03": "2026-10-03", "1.2.2026": "2026-02-01", "v3.1": "v3.1", "": "",
	} {
		if got := YMD(in); got != want {
			t.Errorf("YMD(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRelevance(t *testing.T) {
	names := []string{"Titan Camera 5.0", "Android Titan", "Titan", "Bad-Droid Titan Pack", "Titanic", "Tank"}
	got := byRelevance(names, "titan", func(s string) string { return s }, func(string) string { return "" }, func(string) int { return 0 })
	want := []string{"Titan", "Titan Camera 5.0", "Android Titan", "Bad-Droid Titan Pack", "Titanic"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if relevance("quick draw", "Quick Draw Mod", "") <= relevance("quick draw", "Draw Quickly", "") {
		t.Error("phrase start should beat scattered words")
	}
	if relevance("jujutsu 01", "Jujutsu Playground", "01 STUDIO") == 0 {
		t.Error("author words should count")
	}
}
