//go:build linux || darwin

package blockcache

import (
	"os"
	"syscall"
)

const noFollow = syscall.O_NOFOLLOW

func validFile(_ *os.File, i os.FileInfo) bool {
	s, ok := i.Sys().(*syscall.Stat_t)
	return i.Mode().IsRegular() && i.Mode().Perm()&0077 == 0 && ok && s.Nlink == 1 && s.Uid == uint32(os.Getuid())
}
