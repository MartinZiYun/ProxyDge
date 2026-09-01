//go:build !windows

package main

// resolveMappedDrive is a no-op on non-Windows platforms.
func resolveMappedDrive(path string) string {
	return ""
}
