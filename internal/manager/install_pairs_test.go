package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateLockExclusive(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	l, err := LockState(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var nilLock *StateLock
	nilLock.Unlock() // must not panic

	// A second process (or thread) cannot take it while it is held.
	_, err = LockState(50 * time.Millisecond)
	if err == nil {
		t.Fatal("second lock while held must fail")
	}
	if !strings.Contains(err.Error(), "busy") {
		t.Fatalf("unclear lock error: %v", err)
	}

	// Unlocking lets the next one in.
	l.Unlock()
	l2, err := LockState(50 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	l2.Unlock()
}

func TestInstallDuplicateContraptionNames(t *testing.T) {
	m := newTestManager(t)
	// Three contraptions; two of them sanitise to the same folder name. The
	// first "tank" must end up in tank/, and "other" must hold Gamma's
	// files, not whichever root the pairing happened to land on.
	src := zipOf(t, map[string]string{
		"Alpha/tank.jaap":  "first",
		"Alpha/tank.json":  "{}",
		"Beta/tank.jaap":   "second",
		"Beta/tank.json":   "{}",
		"Gamma/other.jaap": "third",
		"Gamma/other.json": "{}",
	})
	if err := m.Install(&Candidate{Key: "gb:2", Name: "Tanks", Path: src}); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, m.ContraptionsDir); len(got) != 2 {
		t.Fatalf("contraption folders: %v", got)
	}
	first, err := os.ReadFile(filepath.Join(m.ContraptionsDir, "tank", "tank.jaap"))
	if err != nil || string(first) != "first" {
		t.Fatalf("tank.jaap: %q, %v", first, err)
	}
	third, err := os.ReadFile(filepath.Join(m.ContraptionsDir, "other", "other.jaap"))
	if err != nil || string(third) != "third" {
		t.Fatalf("other/other.jaap: %q, %v (the duplicate-name pairing mixed contraptions up)", third, err)
	}
	inst := m.State.Mods["gb:2"]
	if inst == nil || len(inst.Folders) != 2 {
		t.Fatalf("installed: %+v", inst)
	}
	probs, err := m.Verify()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range probs {
		if p.Bad {
			t.Fatalf("verify: %+v", p)
		}
	}
}
