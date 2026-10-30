package winfsp

import (
	"errors"
	"os"
	"strings"
)

func Preflight(path string) error {
	if len(path) == 2 && path[1] == ':' {
		if _, err := os.Stat(path + "\\"); os.IsNotExist(err) {
			return nil
		}
		return errors.New("drive letter is already in use")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("mountpoint must be empty")
	}
	if strings.ContainsAny(path, "\x00") {
		return errors.New("invalid mountpoint")
	}
	return nil
}
