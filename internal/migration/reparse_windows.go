//go:build windows

package migration

import "syscall"

// hasReparsePoint reports whether the path carries the reparse-point
// attribute. NTFS directory junctions and symbolic links both have it, but
// os.Lstat does not report junctions as symlinks on Windows, so the restore
// guard checks the attribute directly. False positives only tighten the
// guard: a reparse point at the destination is refused regardless of its target.
func hasReparsePoint(path string) bool {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attributes, err := syscall.GetFileAttributes(pointer)
	if err != nil {
		return false
	}
	return attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
