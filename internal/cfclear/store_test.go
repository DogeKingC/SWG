package cfclear

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestClearanceStore(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	if c, err := Load("smods.ru"); err != nil || c != nil {
		t.Fatalf("empty store returned %v, %v", c, err)
	}
	if err := Save(&Clearance{Site: "smods.ru", Cookie: "ck", UserAgent: "Browser/1.0", At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	c, err := Load("smods.ru")
	if err != nil || c == nil || c.Cookie != "ck" || c.UserAgent != "Browser/1.0" {
		t.Fatalf("roundtrip: %+v, %v", c, err)
	}
	if runtime.GOOS != "windows" {
		p, _ := path("smods.ru")
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("clearance file must be 0600, got %v", st.Mode())
		}
	}
	if err := Clear("smods.ru"); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load("smods.ru"); c != nil {
		t.Fatal("Clear left a clearance behind")
	}
	if err := Clear("smods.ru"); err != nil { // clearing with nothing stored is fine
		t.Fatal(err)
	}
}
