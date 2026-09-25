//go:build windows

package main

import (
	"fmt"
	"io"
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	detached bool
)

// detachConsole closes the console window Windows opens for a double-clicked
// console program. It does nothing when the program was started from a
// terminal, since the terminal's shell is then attached to the console too.
func detachConsole() {
	var pids [2]uint32
	n, _, _ := kernel32.NewProc("GetConsoleProcessList").Call(uintptr(unsafe.Pointer(&pids[0])), 2)
	if n == 1 {
		kernel32.NewProc("FreeConsole").Call()
		detached = true
	}
}

// reportGUIError shows the error where the user can see it: in a message box
// once the console has gone, otherwise on standard error.
func reportGUIError(stderr io.Writer, err error) {
	fmt.Fprintf(stderr, "error: %v\n", err)
	if !detached {
		return
	}
	text, _ := syscall.UTF16PtrFromString(err.Error())
	title, _ := syscall.UTF16PtrFromString("eBook Converter")
	const mbIconError = 0x10
	user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), mbIconError)
}
