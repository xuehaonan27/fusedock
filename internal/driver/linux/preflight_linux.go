//go:build linux

package linux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Preflight checks the mountpoint and standard FUSE dependencies before
// provider authentication runs. The driver remains authoritative during mount.
func Preflight(mountpoint string) error {
	if _, err := ValidateMountpoint(mountpoint); err != nil {
		return err
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("FUSE driver is unavailable: /dev/fuse does not exist")
		}
		return fmt.Errorf("check /dev/fuse: %w", err)
	}
	if _, err := exec.LookPath("fusermount3"); err == nil {
		return nil
	}
	if _, err := exec.LookPath("fusermount"); err == nil {
		return nil
	}
	return errors.New("FUSE mount utility is unavailable; install the FUSE 3 package")
}
