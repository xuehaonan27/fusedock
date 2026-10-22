//go:build darwin

package fusefs

import (
	"fmt"
	"os"
)

// Preflight checks the mountpoint and common macFUSE locations before provider
// authentication runs. The driver remains authoritative during the real mount.
func Preflight(mountpoint string) error {
	if _, err := ValidateMountpoint(mountpoint); err != nil {
		return err
	}
	for _, path := range []string{
		"/Library/Filesystems/macfuse.fs/Contents/Resources/mount_macfuse",
		"/Library/Filesystems/osxfuse.fs/Contents/Resources/mount_osxfuse",
	} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return nil
		}
	}
	return fmt.Errorf("macFUSE is not installed; install it and enable its kernel extension")
}
