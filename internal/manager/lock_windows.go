//go:build windows

package manager

import (
	"errors"
	"os"
	"syscall"
	"time"
	"unsafe"
)

var errLocked = errors.New("locked by another process")

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x2
	lockfileFailImmediately = 0x1
	errnoLockViolation      = 33 // ERROR_LOCK_VIOLATION: someone else holds it
)

func tryLock(f *os.File) error {
	ol := new(syscall.Overlapped) // zero: lock the whole file from offset 0
	r1, _, err := procLockFileEx.Call(uintptr(f.Fd()),
		lockfileExclusiveLock|lockfileFailImmediately, 0, 0xFFFFFFFF, 0xFFFFFFFF,
		uintptr(unsafe.Pointer(ol)))
	if r1 != 0 {
		return nil
	}
	if errno, ok := err.(syscall.Errno); ok && errno == errnoLockViolation {
		return errLocked
	}
	return err
}

func lockFileRange(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := tryLock(f)
		if err == nil {
			return nil
		}
		if err != errLocked {
			return err
		}
		if time.Now().After(deadline) {
			return lockTimeoutError(timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func unlockFileRange(f *os.File) error {
	ol := new(syscall.Overlapped)
	r1, _, err := procUnlockFileEx.Call(uintptr(f.Fd()), 0, 0xFFFFFFFF, 0xFFFFFFFF,
		uintptr(unsafe.Pointer(ol)))
	if r1 != 0 {
		return nil
	}
	if errno, ok := err.(syscall.Errno); ok && errno == errnoLockViolation {
		return nil // never locked by us; nothing to undo
	}
	return err
}
