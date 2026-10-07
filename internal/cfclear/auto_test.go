package cfclear

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func stubRun(cookie string, err error, calls *atomic.Int32) func(string, Options) (*Result, error) {
	return func(string, Options) (*Result, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond) // the browser takes a moment
		if err != nil {
			return nil, err
		}
		return &Result{Cookies: []Cookie{{Name: "cf_clearance", Value: cookie}}, UserAgent: "Browser/1.0"}, nil
	}
}

// resetAuto gives each test a fresh attempt bookkeeping (it is package
// state, keyed by site, and tests in one process share it).
func resetAuto(t *testing.T) {
	t.Helper()
	autoMu.Lock()
	defer autoMu.Unlock()
	lastAttempt = map[string]attempt{}
	autoRunning = false
	autoDone = nil
}

func TestEnsureStoresAndSingleFlights(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	resetAuto(t)
	var calls atomic.Int32
	run := stubRun("ck-1", nil, &calls)

	// Three concurrent callers: the browser runs once, all get the clearance.
	var wg sync.WaitGroup
	results := make([]bool, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = ensure("smods.ru", "https://x/", Options{}, false, run)
		}(i)
	}
	wg.Wait()
	for i, ok := range results {
		if !ok {
			t.Fatalf("caller %d failed", i)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("browser ran %d times, want 1", calls.Load())
	}
	if c, _ := Load("smods.ru"); c == nil || c.Cookie != "ck-1" {
		t.Fatalf("clearance not stored: %+v", c)
	}
}

func TestEnsureCooldownAfterFailure(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	resetAuto(t)
	var calls atomic.Int32
	run := stubRun("", errors.New("no browser"), &calls)

	if ensure("smods.ru", "https://x/", Options{}, false, run) {
		t.Fatal("a failed attempt must not report success")
	}
	if ensure("smods.ru", "https://x/", Options{}, false, run) {
		t.Fatal("a failed attempt must not report success on retry")
	}
	if calls.Load() != 1 {
		t.Fatalf("the cooldown must stop a second browser run, ran %d times", calls.Load())
	}
	// force (the Settings button) repeats it regardless of the cooldown.
	if ensure("smods.ru", "https://x/", Options{}, true, run) {
		t.Fatal("forced failing attempt must not report success")
	}
	if calls.Load() != 2 {
		t.Fatalf("forced attempt must run, ran %d times", calls.Load())
	}
	if st := Status("smods.ru"); st.OK || st.Error == "" {
		t.Fatalf("status: %+v", st)
	}
}

func TestEnsureAfterSuccessReportsStored(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	resetAuto(t)
	var calls atomic.Int32
	run := stubRun("ck-2", nil, &calls)
	if !ensure("smods.ru", "https://x/", Options{}, false, run) {
		t.Fatal("expected success")
	}
	// Within the cooldown, a stored clearance still counts as usable.
	if !ensure("smods.ru", "https://x/", Options{}, false, run) {
		t.Fatal("a stored clearance must report usable")
	}
	if calls.Load() != 1 {
		t.Fatalf("no second browser run expected, ran %d times", calls.Load())
	}
	// Once it is expired (Cloudflare stops accepting it), a new attempt is
	// allowed right away.
	Expire("smods.ru")
	if !ensure("smods.ru", "https://x/", Options{}, false, run) {
		t.Fatal("expected success after expiry")
	}
	if calls.Load() != 2 {
		t.Fatalf("expired clearance must trigger a new run, ran %d times", calls.Load())
	}
	if c, _ := Load("smods.ru"); c == nil || c.Cookie != "ck-2" {
		t.Fatalf("clearance: %+v", c)
	}
}

func TestEnsureNoCheckServed(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	resetAuto(t)
	var calls atomic.Int32
	run := func(string, Options) (*Result, error) {
		calls.Add(1)
		return &Result{UserAgent: "Browser/1.0"}, nil // no cf_clearance: no check was served
	}
	if !ensure("smods.ru", "https://x/", Options{}, false, run) {
		t.Fatal("a page with no check is usable")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("PPGMODS_HOME"), "clearance-smods.ru.json")); !os.IsNotExist(err) {
		t.Fatal("nothing should be stored when no check was served")
	}
}
