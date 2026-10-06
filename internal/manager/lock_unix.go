//go:build !windows

package manager

import (
	"errors"
	"os"
	"syscall"
	"time"
)

func lockFileRange(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		if time.Now().After(deadline) {
			return lockTimeoutError(timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func unlockFileRange(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
