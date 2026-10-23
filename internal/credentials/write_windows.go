//go:build windows

package credentials

import (
	"errors"

	"golang.org/x/sys/windows"
)

func ensurePrivateDirectory(string) error {
	return errors.New("private broker capability directories are not implemented on Windows; broker setup is disabled")
}

func installPrivate(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncPrivateDirectory(string) error { return nil }
