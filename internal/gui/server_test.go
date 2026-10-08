package gui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Trlydev/SWG/internal/app"
)

func TestHandleUploadLimit(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	old := maxUpload
	maxUpload = 1024
	defer func() { maxUpload = old }()

	s := &server{}
	r := httptest.NewRequest("POST", "/api/upload?name=mod.zip", bytes.NewReader(bytes.Repeat([]byte("x"), 2048)))
	w := httptest.NewRecorder()
	s.handleUpload(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload: code %d, want 413", w.Code)
	}
	if ents, _ := os.ReadDir(uploadDir()); len(ents) != 0 {
		t.Fatalf("oversized upload left files behind: %v", ents)
	}

	// A file that fits is stored and its path returned.
	r = httptest.NewRequest("POST", "/api/upload?name=mod.zip", strings.NewReader("PK\x03\x04"))
	w = httptest.NewRecorder()
	s.handleUpload(w, r)
	if w.Code != 200 {
		t.Fatalf("small upload: code %d: %s", w.Code, w.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out["path"] == "" {
		t.Fatalf("response: %s", w.Body.String())
	}
	os.Remove(out["path"])
}

func TestPruneUploads(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	os.MkdirAll(uploadDir(), 0o755)
	oldPath := filepath.Join(uploadDir(), "up-old.zip")
	os.WriteFile(oldPath, []byte("x"), 0o644)
	stale := time.Now().Add(-48 * time.Hour)
	os.Chtimes(oldPath, stale, stale)
	os.WriteFile(filepath.Join(uploadDir(), "up-new.zip"), []byte("x"), 0o644)

	pruneUploads(24 * time.Hour)

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("old upload not pruned")
	}
	if _, err := os.Stat(filepath.Join(uploadDir(), "up-new.zip")); err != nil {
		t.Fatal("fresh upload was pruned")
	}
}

func TestSettingsRace(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	s := &server{opt: app.DefaultOptions(), quit: make(chan struct{})}

	// Saving settings races with the folder watcher, running jobs and state
	// views that all read the same fields; run with -race to check.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			r := httptest.NewRequest("POST", "/api/settings", strings.NewReader(fmt.Sprintf(`{"cooldown_hours": %d}`, i%72)))
			r.Header.Set("Content-Type", "application/json")
			s.handleSettings(httptest.NewRecorder(), r)
		}
	}()
	for i := 0; i < 50; i++ {
		a := s.newApp(nil)
		_ = a.Opt.Policy.Cooldown
		a = s.newApp(map[string]bool{"allow_high": true})
		if !a.Opt.Policy.AllowHigh {
			t.Fatal("override lost")
		}
	}
	<-done

	// The last save won, and the state view reads it without racing.
	r := httptest.NewRequest("GET", "/api/settings", nil)
	w := httptest.NewRecorder()
	s.handleSettings(w, r)
	var st settings
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	s.handleState(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/state", nil))
}

// The Appearance setting is saved and read back; anything but light or
// "match system" (including no setting at all) means dark.
func TestSettingsTheme(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	s := &server{opt: app.DefaultOptions(), quit: make(chan struct{})}
	defer func() { themeMode = "dark" }()
	for body, want := range map[string]string{
		`{"theme":"light"}`:  "light",
		`{"theme":"system"}`: "system",
		`{"theme":"pink"}`:   "dark",
		`{}`:                 "dark",
	} {
		r := httptest.NewRequest("POST", "/api/settings", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handleSettings(w, r)
		var st settings
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.Theme != want {
			t.Errorf("%s: theme %q, want %q (%v)", body, st.Theme, want, err)
		}
		saved, err := loadSettings()
		if err != nil || themeSetting(saved.Theme) != want {
			t.Errorf("%s: saved %+v", body, saved)
		}
	}
}
