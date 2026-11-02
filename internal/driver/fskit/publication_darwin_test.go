package fskit_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	native "github.com/xuehaonan27/fusedock/internal/driver/fskit"
	"github.com/xuehaonan27/fusedock/internal/providers/local"
	"github.com/xuehaonan27/fusedock/internal/vfs"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func TestFSKitRootNamespacePublication(t *testing.T) {
	if os.Getenv("FUSEDOCK_FSKIT_TEST") != "1" {
		t.Skip("requires installed and enabled FSKit extension")
	}
	if macOSMajor(t) < 27 {
		t.Skip("requires macOS 27 namespace publication")
	}
	source, point := t.TempDir(), t.TempDir()
	original := bytes.Repeat([]byte("A"), (8<<20)+17)
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("seed.txt", original)
	base, err := local.New().Open(context.Background(), provider.OpenRequest{Options: map[string]string{"root": source}})
	if err != nil {
		t.Fatal(err)
	}
	session := vfs.New(base, 0)
	defer session.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- native.Mount(ctx, session, point, native.Options{ReadOnly: true, EntryTTL: 50 * time.Millisecond, Ready: func() { close(ready) }})
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Error("unmount timed out")
		}
	}()
	select {
	case <-ready:
	case err := <-done:
		done <- nil
		t.Fatalf("mount: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("mount timed out")
	}
	waitFor := func(label string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(label)
	}
	absent := func(name string) bool {
		_, err := os.Stat(filepath.Join(point, name))
		return errors.Is(err, os.ErrNotExist)
	}
	content := func(name, expected string) bool {
		b, err := os.ReadFile(filepath.Join(point, name))
		return err == nil && string(b) == expected
	}
	old, err := os.Open(filepath.Join(point, "seed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var first [1]byte
	if _, err := old.ReadAt(first[:], 0); err != nil || first[0] != 'A' {
		t.Fatalf("initial read: %v", err)
	}
	if !absent("added.txt") || !absent("folder") {
		t.Fatal("fixture names already exist")
	}
	write("added.txt", []byte("added"))
	waitFor("negative cached name did not appear", func() bool { return content("added.txt", "added") })
	if err := os.Mkdir(filepath.Join(source, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	waitFor("new directory did not appear", func() bool { info, err := os.Stat(filepath.Join(point, "folder")); return err == nil && info.IsDir() })
	oldDirectory, err := os.Open(filepath.Join(point, "folder"))
	if err != nil {
		t.Fatal(err)
	}
	defer oldDirectory.Close()
	if !absent("renamed.txt") {
		t.Fatal("rename destination already exists")
	}
	if err := os.Rename(filepath.Join(source, "added.txt"), filepath.Join(source, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	waitFor("rename did not update both names", func() bool { return absent("added.txt") && content("renamed.txt", "added") })
	// Atomic replacement of the provider's file preserves its already open FD.
	write("next.txt", bytes.Repeat([]byte("B"), len(original)))
	if err := os.Rename(filepath.Join(source, "next.txt"), filepath.Join(source, "seed.txt")); err != nil {
		t.Fatal(err)
	}
	waitFor("same-size replacement did not appear", func() bool {
		b, err := os.ReadFile(filepath.Join(point, "seed.txt"))
		return err == nil && bytes.Equal(b, bytes.Repeat([]byte("B"), len(original)))
	})
	if err := os.Remove(filepath.Join(source, "seed.txt")); err != nil {
		t.Fatal(err)
	}
	waitFor("removed open file is still named", func() bool { return absent("seed.txt") })
	var tail [4]byte
	if _, err := old.ReadAt(tail[:], 8<<20); err != nil || string(tail[:]) != "AAAA" {
		t.Fatalf("retained uncached bytes after deletion: %q %v", tail, err)
	}
	info, err := old.Stat()
	if err != nil || info.Size() != int64(len(original)) {
		t.Fatalf("retained size: %v %v", info, err)
	}
	if n, err := old.ReadAt(first[:], int64(len(original))); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("retained EOF: %d %v", n, err)
	}
	if err := os.Remove(filepath.Join(source, "folder")); err != nil {
		t.Fatal(err)
	}
	write("folder", []byte("now a file"))
	waitFor("directory-to-file replacement failed", func() bool { return content("folder", "now a file") })
	if info, err := oldDirectory.Stat(); err != nil || !info.IsDir() {
		t.Fatalf("retained directory changed its type: %v %v", info, err)
	}
	waitFor("directory contains stale or internal names", func() bool {
		entries, err := os.ReadDir(point)
		if err != nil {
			return false
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return reflect.DeepEqual(names, []string{"folder", "renamed.txt"})
	})
}
