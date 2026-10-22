//go:build windows

package credentials

import (
	"errors"
	"os"
)

func checkOwnerAndMode(_ string, _ os.FileInfo) error {
	return errors.New("credential-file owner checks are not implemented on Windows; use --credential-env for now")
}
