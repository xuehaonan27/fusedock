package winfsp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func Preflight(path string) error {
	if len(path) == 2 && path[1] == ':' {
		if _, err := os.Stat(path + "\\"); os.IsNotExist(err) {
			return nil
		}
		return errors.New("drive letter is already in use")
	}
	if strings.ContainsAny(path, "\x00") {
		return errors.New("invalid mountpoint")
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("WinFsp creates its directory mountpoint; choose a path that does not exist")
	} else if !os.IsNotExist(err) {
		return err
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !parent.IsDir() {
		return errors.New("mountpoint parent must be a directory")
	}
	return nil
}
