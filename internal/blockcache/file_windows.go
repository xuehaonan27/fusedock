//go:build windows

package blockcache

import (
	"golang.org/x/sys/windows"
	"os"
)

const noFollow = 0

func validFile(f *os.File, i os.FileInfo) bool {
	var data windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &data) != nil {
		return false
	}
	return i.Mode().IsRegular() && data.NumberOfLinks == 1 && data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}
