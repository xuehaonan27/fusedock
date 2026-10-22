//go:build !windows

package accounts

import (
	"errors"
	"os"
)

func checkConfigPermissions(info os.FileInfo) error {
	if info.Mode().Perm() != 0o600 {
		return errors.New("account store must have mode 0600")
	}
	return nil
}
