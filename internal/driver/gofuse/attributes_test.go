//go:build linux || darwin

package gofuse

import (
	"context"
	"errors"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"syscall"
	"testing"
	"time"
)

func TestAttributes(t *testing.T) {
	object := provider.Object{ID: "file-1", Name: "a", Kind: provider.KindFile, Size: 513, Modified: time.Unix(100, 0)}
	var attr fuse.Attr
	fillAttr(&attr, object)
	if attr.Size != 513 || attr.Mode&fuse.S_IFREG == 0 || attr.Blocks != 2 {
		t.Fatalf("unexpected attributes: %+v", attr)
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
		{provider.ErrConflict, syscall.ESTALE},
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
