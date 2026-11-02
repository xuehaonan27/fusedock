package fskit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	native "github.com/xuehaonan27/fusedock/internal/driver/fskit"
	"github.com/xuehaonan27/fusedock/internal/providers/local"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func TestFSKitLocalReadRemountContract(t *testing.T) {
	if os.Getenv("FUSEDOCK_FSKIT_TEST") != "1" {
		t.Skip("requires installed and enabled FSKit extension")
	}
	root, point := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"initial content", "updated content after remount"} {
		for _, relative := range []string{"file.txt", filepath.Join("nested", "file.txt")} {
			if err := os.WriteFile(filepath.Join(root, relative), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if !t.Run(content, func(t *testing.T) { testLocalMount(t, root, point, content) }) {
			return
		}
	}
}

func testLocalMount(t *testing.T, root, point, content string) {
	t.Helper()
	session, err := local.New().Open(context.Background(), provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- native.Mount(ctx, session, point, native.Options{ReadOnly: true, EntryTTL: time.Second, Ready: func() { close(ready) }})
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Error("native unmount timed out")
		}
	}()
	select {
	case <-ready:
	case err := <-done:
		done <- nil
		t.Fatalf("native mount: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("native mount did not become ready")
	}
	for path, count := range map[string]int{point: 2, filepath.Join(point, "nested"): 1} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != count {
			t.Fatalf("directory listing: %v %v", entries, err)
		}
	}
	for _, relative := range []string{"file.txt", filepath.Join("nested", "file.txt")} {
		path := filepath.Join(point, relative)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatalf("file %s: %q %v", relative, got, err)
		}
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			file.Close()
			t.Fatal("read-only file opened for writing")
		}
	}
	// Directory mode allows the host's one-use publication to reach FSKit.
	// Public namespace mutations must still be rejected by the module.
	file := filepath.Join(point, "file.txt")
	for name, mutate := range map[string]func() error{
		"create":             func() error { return os.WriteFile(filepath.Join(point, "created.txt"), []byte("no"), 0600) },
		"mkdir":              func() error { return os.Mkdir(filepath.Join(point, "created"), 0700) },
		"rename":             func() error { return os.Rename(file, filepath.Join(point, "renamed.txt")) },
		"unlink":             func() error { return os.Remove(file) },
		"forged publication": func() error { return os.Rename(file, filepath.Join(point, ".fusedock-publish-forged")) },
	} {
		if err := mutate(); err == nil {
			t.Errorf("read-only mount accepted %s", name)
		}
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != content {
		t.Fatalf("rejected mutations changed file: %q %v", got, err)
	}
}
