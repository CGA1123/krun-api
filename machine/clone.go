//go:build darwin

package machine

import "golang.org/x/sys/unix"

// cloneDir creates an APFS copy-on-write clone of src at dst.
// dst must not already exist. Both paths must be on an APFS volume.
func cloneDir(src, dst string) error {
	return unix.Clonefileat(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, 0)
}
