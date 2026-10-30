package credentials

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivateFileDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "test.credentials")
	if err := WritePrivate(path, map[string][]byte{"password": []byte("fixture")}); err != nil {
		t.Fatal(err)
	}
	values, err := Read(path)
	if err != nil || string(values["password"]) != "fixture" {
		t.Fatalf("private read: %v", err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("broad DACL was accepted")
	}
}

func TestPrivateFileRejectsHardLinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "test.credentials")
	if err := WritePrivate(path, map[string][]byte{"password": []byte("fixture")}); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, path+".link"); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("multiply-linked credential file accepted")
	}
}
