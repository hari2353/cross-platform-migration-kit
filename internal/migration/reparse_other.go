//go:build !windows

package migration

// hasReparsePoint has no meaning off Windows; NTFS junctions do not exist.
func hasReparsePoint(string) bool { return false }
