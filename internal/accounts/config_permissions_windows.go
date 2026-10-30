//go:build windows

package accounts

import (
	"github.com/xuehaonan27/fusedock/internal/credentials"
	"os"
)

func checkConfigPermissions(file *os.File, _ os.FileInfo) error {
	return credentials.CheckPrivateFile(file)
}
