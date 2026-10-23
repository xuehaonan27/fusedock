package webdavfs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/xuehaonan/fusedock/internal/providers/local"
	"github.com/xuehaonan/fusedock/pkg/provider"
)

func TestFileSystemReadSeekAndETag(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data := []byte("0123456789abcdef")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), data, 0600); err != nil {
		t.Fatal(err)
	}
	session, err := local.Factory{}.Open(ctx, provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	fs, err := New(ctx, session, "/")
	if err != nil {
		t.Fatal(err)
	}
	info, err := fs.Stat(ctx, "/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	firstTag, err := info.(interface {
		ETag(context.Context) (string, error)
	}).ETag(ctx)
	if err != nil || firstTag == "" {
		t.Fatalf("ETag=(%q,%v)", firstTag, err)
	}
	secondTag, _ := info.(interface {
		ETag(context.Context) (string, error)
	}).ETag(ctx)
	if firstTag != secondTag {
		t.Fatal("ETag changed without a content version change")
	}
	f, err := fs.OpenFile(ctx, "/a.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if offset, err := f.Seek(10, io.SeekStart); err != nil || offset != 10 {
		t.Fatalf("Seek=(%d,%v)", offset, err)
	}
	buf := make([]byte, 4)
	if n, err := f.Read(buf); n != 4 || err != nil || string(buf) != "abcd" {
		t.Fatalf("Read=(%d,%v) %q", n, err, buf)
	}
	if offset, err := f.Seek(-2, io.SeekEnd); err != nil || offset != 14 {
		t.Fatalf("Seek end=(%d,%v)", offset, err)
	}
	if n, err := f.Read(buf[:1]); n != 1 || err != nil || string(buf[:1]) != "e" {
		t.Fatalf("tail read=(%d,%v) %q", n, err, buf[:1])
	}
	if _, err := f.Write([]byte("x")); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("write error = %v", err)
	}
}

func TestFileSystemDirectoryPagingAndReadOnlyMutations(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	session, err := local.Factory{}.Open(ctx, provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	fs, err := New(ctx, session, "/")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := fs.OpenFile(ctx, "/", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	page, err := dir.Readdir(2)
	if err != nil || len(page) != 2 {
		t.Fatalf("first page=(%d,%v)", len(page), err)
	}
	page, err = dir.Readdir(2)
	if err != nil || len(page) != 1 {
		t.Fatalf("second page=(%d,%v)", len(page), err)
	}
	if _, err := dir.Readdir(1); !errors.Is(err, io.EOF) {
		t.Fatalf("final Readdir error = %v", err)
	}
	if err := fs.Mkdir(ctx, "/x", 0700); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Mkdir error = %v", err)
	}
	if err := fs.RemoveAll(ctx, "/a.txt"); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("RemoveAll error = %v", err)
	}
	if err := fs.Rename(ctx, "/a.txt", "/d.txt"); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Rename error = %v", err)
	}
	if _, err := fs.OpenFile(ctx, "/a.txt", os.O_WRONLY, 0); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("writable OpenFile error = %v", err)
	}
	if _, err := fs.Stat(ctx, "/../outside"); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("traversal error = %v", err)
	}
}
