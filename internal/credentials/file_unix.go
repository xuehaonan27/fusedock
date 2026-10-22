//go:build !windows

package credentials

import (
	"errors"
	"os"
	"syscall"
)

func checkOwnerAndMode(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("credential file permissions are too broad; run chmod 600 on the file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot verify credential file owner")
	}
	if int(stat.Uid) != os.Geteuid() {
		return errors.New("credential file must be owned by the current user")
	}
	return nil
}
