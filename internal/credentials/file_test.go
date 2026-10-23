//go:build !windows

package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPrivateKeyValueFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pku.credentials")
	data := "# PKU\nusername=student\npassword=a=b c\nendpoint=https://disk.pku.edu.cn\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clearValues(values)
	if string(values["username"]) != "student" || string(values["password"]) != "a=b c" {
		t.Fatalf("unexpected values: %#v", values)
	}
}

func TestReadRejectsPublicPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pku.credentials")
	if err := os.WriteFile(path, []byte("password=x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("world-readable credential file was accepted")
	}
}

func TestReadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("password=x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Read(link); err == nil {
		t.Fatal("symlink credential file was accepted")
	}
}

func TestReadRejectsDuplicateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pku.credentials")
	if err := os.WriteFile(path, []byte("password=one\npassword=two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("duplicate credential was accepted")
	}
}

func TestWritePrivateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "broker.credentials")
	values := map[string][]byte{
		"provider": []byte("broker"),
		"endpoint": []byte("http://127.0.0.1:43210"),
		"token":    []byte("capability"),
	}
	if err := WritePrivate(path, values); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	read, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	defer clearValues(read)
	for key, value := range values {
		if string(read[key]) != string(value) {
			t.Errorf("%s = %q", key, read[key])
		}
	}
}

func TestWritePrivateRejectsBroadDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(filepath.Join(dir, "broker.credentials"), map[string][]byte{"token": []byte("secret")}); err == nil {
		t.Fatal("wrote capability into a group/world-accessible directory")
	}
}

func clearValues(values map[string][]byte) {
	for key, value := range values {
		clear(value)
		delete(values, key)
	}
}
