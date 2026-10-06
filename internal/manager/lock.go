package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A state lock keeps two ppgmods processes (the window and a command-line
// run) from loading, changing and saving state.json at the same time: a save
// writes the whole file, so two writers would undo each other (a removed mod
// coming back, an installed one disappearing). The lock is a small file
// locked with flock/LockFileEx, which the operating system releases if the
// process dies, so a crash cannot leave it locked for ever.
//
// Only operations that change state.json need it: readers (the window's
// state view, verify) see old or new saves, never partial ones, because
// Save() renames a finished file into place.

// StateLock is a held state lock. Unlock it when the state-changing
// operation is done.
type StateLock struct {
	f *os.File
}

// LockState acquires an exclusive state lock, waiting up to timeout for
// another ppgmods process to finish what it is doing.
func LockState(timeout time.Duration) (*StateLock, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFileRange(f, timeout); err != nil {
		f.Close()
		return nil, err
	}
	return &StateLock{f}, nil
}

// Unlock releases the state lock.
func (l *StateLock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	unlockFileRange(l.f)
	l.f.Close()
	l.f = nil
}

func lockTimeoutError(timeout time.Duration) error {
	return fmt.Errorf("another ppgmods is busy with the mod list (waited %s); let it finish and try again", timeout)
}
