//go:build !windows

package accounts

import (
	"errors"
	"github.com/xuehaonan27/fusedock/internal/credentials"
	"os"
)

func checkConfigPermissions(file *os.File, info os.FileInfo) error {
	if info.Mode().Perm() != 0o600 {
		return errors.New("account store must have mode 0600")
	}
	return credentials.CheckPrivateFile(file)
}
