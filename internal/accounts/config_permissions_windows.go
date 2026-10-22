//go:build windows

package accounts

import "os"

// Windows account metadata relies on the current-user directory profile ACL.
// A protected credential ACL will be added with the Windows DPAPI backend.
func checkConfigPermissions(os.FileInfo) error { return nil }
