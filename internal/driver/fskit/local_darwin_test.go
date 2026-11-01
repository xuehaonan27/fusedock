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

func TestFSKitNestedReadContract(t *testing.T) {
	if os.Getenv("FUSEDOCK_FSKIT_TEST") != "1" {
		t.Skip("requires installed and enabled FSKit extension")
	}
	root, point := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "file.txt"), []byte("nested read"), 0600); err != nil {
		t.Fatal(err)
	}
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
	for _, path := range []string{point, filepath.Join(point, "nested")} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 1 {
			t.Fatalf("directory listing: %v %v", entries, err)
		}
	}
	path := filepath.Join(point, "nested", "file.txt")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "nested read" {
		t.Fatalf("nested file: %q %v", got, err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err == nil {
		file.Close()
		t.Fatal("read-only file opened for writing")
	}
}
