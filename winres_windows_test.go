package main

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// TestWindowsResources asks Windows for the version information of the
// running test binary, which carries the same resources as leafbind.exe
// when rsrc_windows_*.syso has been generated (go run ./tools/packaging
// winres). Without them it is skipped.
func TestWindowsResources(t *testing.T) {
	if _, err := os.Stat("rsrc_windows_" + runtime.GOARCH + ".syso"); err != nil {
		t.Skip("no Windows resources generated")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dll := syscall.NewLazyDLL("version.dll")
	size, _, _ := dll.NewProc("GetFileVersionInfoSizeW").Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(exe))), 0)
	if size == 0 {
		t.Fatal("the executable has no version information")
	}
	buf := make([]byte, size)
	if ok, _, err := dll.NewProc("GetFileVersionInfoW").Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(exe))), 0, size, uintptr(unsafe.Pointer(&buf[0]))); ok == 0 {
		t.Fatal(err)
	}
	query := func(path string) string {
		var p unsafe.Pointer
		var n uint32
		ok, _, _ := dll.NewProc("VerQueryValueW").Call(uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(path))), uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&n)))
		if ok == 0 || n == 0 {
			return ""
		}
		return syscall.UTF16ToString(unsafe.Slice((*uint16)(p), n))
	}
	if got := query(`\StringFileInfo\040904B0\ProductName`); got != "Leafbind" {
		t.Errorf("ProductName = %q, want Leafbind", got)
	}
	if got := query(`\StringFileInfo\040904B0\OriginalFilename`); got != "leafbind.exe" {
		t.Errorf("OriginalFilename = %q", got)
	}

	// The icon group loads as an icon.
	user32 := syscall.NewLazyDLL("user32.dll")
	mod, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	icon, _, err := user32.NewProc("LoadIconW").Call(mod, 1) // MAKEINTRESOURCE(1)
	if icon == 0 {
		t.Errorf("the icon does not load: %v", err)
	}
}
