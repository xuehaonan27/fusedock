package nativetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/xuehaonan27/fusedock/internal/vfs"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type currentRemote struct{ *remote }

func (*currentRemote) ReadCapabilities(context.Context) (provider.ReadCapabilities, error) {
	return provider.ReadCapabilities{StableObjectID: true}, nil
}

// RunRemountRefresh checks the FSKit 26 contract: a kernel item retains its
// observed revision, and remounting is the explicit content refresh boundary.
func RunRemountRefresh(t *testing.T, mount Mount) {
	t.Helper()
	t.Run("revision-provider", func(t *testing.T) { runRemountRefresh(t, mount, true) })
	t.Run("current-content-provider", func(t *testing.T) { runRemountRefresh(t, mount, false) })
}

func runRemountRefresh(t *testing.T, mount Mount, exact bool) {
	t.Helper()
	original := append([]byte("original"), bytes.Repeat([]byte("A"), 8<<20)...)
	modified := append([]byte("modified with a new size"), bytes.Repeat([]byte("B"), 8<<20)...)
	base := &remote{version: "v1", name: "file.txt", objectID: "file", content: map[string][]byte{"v1": original, "v2": modified}}
	var source provider.Session = base
	if !exact {
		source = &currentRemote{base}
	}
	point := t.TempDir()
	path := filepath.Join(point, "file.txt")
	for _, revision := range []string{"v1", "v2"} {
		t.Run(revision, func(t *testing.T) {
			session := vfs.New(source, 0)
			defer session.Close()
			ctx, cancel := context.WithCancel(context.Background())
			ready := make(chan struct{})
			done := make(chan error, 1)
			go func() { done <- mount(ctx, session, point, func() { close(ready) }) }()
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
			entries, err := os.ReadDir(point)
			if err != nil || len(entries) != 1 || entries[0].Name() != "file.txt" {
				t.Fatalf("directory listing: %v %v", entries, err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			want := base.content[revision]
			prefix := make([]byte, 4)
			if _, err := file.ReadAt(prefix, 0); err != nil || !bytes.Equal(prefix, want[:4]) {
				t.Fatalf("prefix: %q %v", prefix, err)
			}
			if revision == "v1" {
				base.mu.Lock()
				base.version = "v2"
				base.mu.Unlock()
				session.Invalidate("")
				if _, err := os.ReadDir(point); err != nil {
					t.Fatal(err)
				}
			}
			// Closing one open must retain access for the other one. The next
			// read reaches a range not read before the provider changed.
			second, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := second.Close(); err != nil {
				t.Fatal(err)
			}
			for _, offset := range []int64{(4 << 20) - 3, 8 << 20} {
				buf := make([]byte, 8)
				if _, err := file.ReadAt(buf, offset); err != nil || !bytes.Equal(buf, want[offset:offset+8]) {
					t.Fatalf("range at %d: %q %v", offset, buf, err)
				}
			}
			info, err := file.Stat()
			if err != nil || info.Size() != int64(len(want)) {
				t.Fatalf("snapshot attributes: %v %v", info, err)
			}
			if n, err := file.ReadAt(prefix, int64(len(want))); n != 0 || !errors.Is(err, io.EOF) {
				t.Fatalf("EOF: %d %v", n, err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if !exact && revision == "v1" {
				if !errors.Is(err, syscall.ESTALE) {
					t.Fatalf("changed current-content provider must report ESTALE: %v", err)
				}
			} else if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("close and reopen: got %d bytes, want %d: %v", len(got), len(want), err)
			}
			writable, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err == nil {
				writable.Close()
				t.Fatal("read-only file opened for writing")
			}
		})
		if t.Failed() {
			return
		}
	}
}
