//go:build windows

package main

import (
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procQueryDosDevice = kernel32.NewProc("QueryDosDeviceW")
)

// resolveMappedDrive checks if path starts with a Windows mapped drive
// (e.g. subst R: D:\real\path) and returns the resolved real path.
// If the drive is not mapped, returns "".
func resolveMappedDrive(path string) string {
	if len(path) < 2 || path[1] != ':' {
		return ""
	}
	drive := strings.ToUpper(path[:2]) // "R:"

	var buf [260]uint16
	r, _, _ := procQueryDosDevice.Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(drive))),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r == 0 {
		return ""
	}

	devicePath := syscall.UTF16ToString(buf[:])

	// subst mappings return "\??\D:\real\path"
	if strings.HasPrefix(devicePath, `\??\`) {
		devicePath = devicePath[4:]
	} else {
		// Real disk partition like "\Device\HarddiskVolume1" — not a subst mapping
		return ""
	}

	// Replace the drive letter with the real path
	return devicePath + path[2:]
}
