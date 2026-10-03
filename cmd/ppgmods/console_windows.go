//go:build windows

package main

import (
	"os"
	"syscall"
)

// Release builds use the Windows GUI subsystem so double-clicking does not
// open a console. When run from a terminal with a command, reattach to that
// terminal so output still appears.
func attachConsole() {
	k := syscall.NewLazyDLL("kernel32.dll")
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := k.NewProc("AttachConsole").Call(attachParentProcess); r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = f
	}
}
