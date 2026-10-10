package cfclear

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeRun replaces the browser: each call is recorded and answered by
// answer (called with the options the browser would get).
type fakeRun struct {
	mu     sync.Mutex
	calls  []Options
	answer func(Options) (*Result, error)
}

func (f *fakeRun) run(u string, opt Options) (*Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, opt)
	f.mu.Unlock()
	return f.answer(opt)
}

// useFake gives a test a fresh attempt record and a fake browser.
func useFake(t *testing.T, answer func(Options) (*Result, error)) *fakeRun {
	t.Helper()
	t.Setenv("PPGMODS_HOME", t.TempDir())
	autoMu.Lock()
	lastAttempt = map[string]attempt{}
	autoRunning, checking = 0, false
	autoMu.Unlock()
	goRejectedAt.Store(0)
	SetAuto(true)
	f := &fakeRun{answer: answer}
	old := run
	run = f.run
	t.Cleanup(func() { run = old })
	return f
}

func passed(Options) (*Result, error) {
	return &Result{Cookies: []Cookie{{Name: "cf_clearance", Value: "ck-1"}}, UserAgent: "Browser/1.0", HTML: "<html>page</html>"}, nil
}

// A page read through the browser comes back, on ppgmods' own profile, and
// the clearance it carries is stored.
func TestBrowserGetStores(t *testing.T) {
	f := useFake(t, passed)
	b, err := browserGet("smods.ru", "https://x/", nil, "/usr/bin/google-chrome")
	if err != nil || string(b) != "<html>page</html>" {
		t.Fatalf("got %q, %v", b, err)
	}
	if len(f.calls) != 1 || f.calls[0].Headed {
		t.Fatalf("want one background run, got %+v", f.calls)
	}
	if want := filepath.Join(os.Getenv("PPGMODS_HOME"), "browser", "ppgmods-google-chrome"); f.calls[0].ProfileDir != want {
		t.Fatalf("profile %q, want %q", f.calls[0].ProfileDir, want)
	}
	if c, _ := Load("smods.ru"); c == nil || c.Cookie != "ck-1" || c.UserAgent != "Browser/1.0" {
		t.Fatalf("clearance not stored: %+v", c)
	}
	if st := Status("smods.ru"); !st.OK {
		t.Fatalf("status: %+v", st)
	}
}

// A check that wants a person is not retried in the background, and no
// browser starts again during the cooldown.
func TestBrowserGetNeedsPersonCooldown(t *testing.T) {
	f := useFake(t, func(Options) (*Result, error) { return nil, ErrNeedsPerson })
	for i := 0; i < 3; i++ {
		if _, err := browserGet("smods.ru", "https://x/", nil, "/usr/bin/chromium"); !errors.Is(err, ErrNeedsPerson) {
			t.Fatalf("read %d: want ErrNeedsPerson, got %v", i, err)
		}
	}
	if len(f.calls) != 1 {
		t.Fatalf("the browser ran %d times, want 1 (cooldown)", len(f.calls))
	}
	if st := Status("smods.ru"); st.OK || !st.NeedsPerson {
		t.Fatalf("status: %+v", st)
	}
}

// A browser that failed for another reason gets one more try.
func TestBrowserGetRetriesOnce(t *testing.T) {
	f := useFake(t, func(Options) (*Result, error) { return nil, errors.New("crashed") })
	if _, err := browserGet("smods.ru", "https://x/", nil, "/usr/bin/chromium"); err == nil {
		t.Fatal("want an error")
	}
	if len(f.calls) != 2 {
		t.Fatalf("the browser ran %d times, want 2", len(f.calls))
	}
}

// With the automatic check off in Settings, no browser starts.
func TestBrowserGetOff(t *testing.T) {
	f := useFake(t, passed)
	SetAuto(false)
	defer SetAuto(true)
	if _, err := browserGet("smods.ru", "https://x/", nil, "/usr/bin/chromium"); err == nil {
		t.Fatal("want an error with the check off")
	}
	if len(f.calls) != 0 {
		t.Fatal("no browser may start with the check off")
	}
}

// The Settings button: background first; when the check wants a person, a
// window. It ignores the cooldown of the background reads.
func TestCheckOpensWindowForPerson(t *testing.T) {
	var ticked atomic.Bool // the person ticked the box: the profile keeps the clearance
	f := useFake(t, func(o Options) (*Result, error) {
		if o.Headed {
			ticked.Store(true)
		}
		if !ticked.Load() {
			return nil, ErrNeedsPerson
		}
		return passed(o)
	})
	browserGet("smods.ru", "https://x/", nil, "/usr/bin/chromium") // starts a cooldown
	if err := check("smods.ru", "https://x/", nil, "/usr/bin/chromium"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 || f.calls[1].Headed || !f.calls[2].Headed {
		t.Fatalf("want background, background, window; got %+v", f.calls)
	}
	if st := Status("smods.ru"); !st.OK {
		t.Fatalf("status: %+v", st)
	}
	// Once passed, background reads work again straight away.
	if _, err := browserGet("smods.ru", "https://x/", nil, "/usr/bin/chromium"); err != nil {
		t.Fatal(err)
	}
}

// A check that passes in the background doesn't open a window.
func TestCheckBackgroundOnly(t *testing.T) {
	f := useFake(t, passed)
	if err := check("smods.ru", "https://x/", nil, "/usr/bin/chromium"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].Headed {
		t.Fatalf("got %+v", f.calls)
	}
}

// A clearance Cloudflare refused from ppgmods' client isn't offered again.
func TestRefusedClearanceNotOffered(t *testing.T) {
	useFake(t, passed)
	WireSkymods(nil)
	if err := Save(&Clearance{Site: SkymodsSite, Cookie: "ck", UserAgent: "UA"}); err != nil {
		t.Fatal(err)
	}
	if ck, _ := skyClearance(); ck != "ck" {
		t.Fatalf("stored clearance not offered: %q", ck)
	}
	goRejectedAt.Store(1) // long ago: offered again
	if ck, _ := skyClearance(); ck != "ck" {
		t.Fatal("an old refusal must not hide the clearance")
	}
	skyClearanceRefused()
	if ck, _ := skyClearance(); ck != "" {
		t.Fatal("a refused clearance must not be offered again")
	}
}

func TestProfileName(t *testing.T) {
	for exe, want := range map[string]string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`: "chrome",
		"/usr/bin/zen-browser": "zen-browser",
		"/snap/bin/firefox":    "firefox",
		"":                     "browser",
	} {
		if got := profileName(filepath.FromSlash(strings.ReplaceAll(exe, `\`, "/"))); got != want {
			t.Errorf("profileName(%q) = %q, want %q", exe, got, want)
		}
	}
}
