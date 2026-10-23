//go:build !windows

package credentials

import (
	"errors"
	"os"
	"syscall"
)

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("credential parent is not a directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("credential directory permissions are too broad")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("credential directory must be owned by the current user")
	}
	return nil
}

func installPrivate(source, target string) error { return os.Rename(source, target) }
func syncPrivateDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
