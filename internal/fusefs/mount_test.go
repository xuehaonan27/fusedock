//go:build linux

package fusefs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

func TestAttributesAndStableInode(t *testing.T) {
	object := provider.Object{ID: "file-1", Name: "a", Kind: provider.KindFile, Size: 513, Modified: time.Unix(100, 0)}
	var attr fuse.Attr
	fillAttr(&attr, object)
	if attr.Size != 513 || attr.Mode&fuse.S_IFREG == 0 || attr.Blocks != 2 {
		t.Fatalf("unexpected attributes: %+v", attr)
	}
	if inode(object.ID) == 0 || inode(object.ID) != inode(object.ID) {
		t.Fatal("inode is not stable")
	}
}

func TestErrnoMapping(t *testing.T) {
	tests := []struct {
		err  error
		want syscall.Errno
	}{
		{provider.ErrNotFound, syscall.ENOENT},
		{provider.ErrPermission, syscall.EACCES},
		{provider.ErrNotDirectory, syscall.ENOTDIR},
		{provider.ErrConflict, syscall.EBUSY},
		{provider.ErrUnsupported, syscall.ENOSYS},
		{context.Canceled, syscall.EINTR},
		{context.DeadlineExceeded, syscall.ETIMEDOUT},
		{errors.New("other"), syscall.EIO},
	}
	for _, test := range tests {
		if got := errno(test.err); got != test.want {
			t.Errorf("errno(%v)=%v, want %v", test.err, got, test.want)
		}
	}
}

func TestMountRejectsUnrequestedWritableMode(t *testing.T) {
	err := Mount(context.Background(), &dummySession{}, t.TempDir(), Options{})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountErrorAddsDriverHint(t *testing.T) {
	err := mountError(errors.New("no FUSE mount utility found"))
	if !strings.Contains(err.Error(), "FUSE") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMountpoint(t *testing.T) {
	empty := t.TempDir()
	point, err := ValidateMountpoint(empty)
	if err != nil || point == "" {
		t.Fatalf("empty mountpoint rejected: %v", err)
	}
	nonempty := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonempty, "file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateMountpoint(nonempty); err == nil {
		t.Fatal("nonempty mountpoint was accepted")
	}
}

type dummySession struct{}

func (*dummySession) Root(context.Context) (provider.Object, error) { return provider.Object{}, nil }
func (*dummySession) Stat(context.Context, provider.Object) (provider.Object, error) {
	return provider.Object{}, nil
}
func (*dummySession) List(context.Context, provider.Object) ([]provider.Object, error) {
	return nil, nil
}
func (*dummySession) OpenRead(context.Context, provider.Object) (provider.Reader, error) {
	return nil, provider.ErrUnsupported
}
func (*dummySession) Close() error { return nil }
