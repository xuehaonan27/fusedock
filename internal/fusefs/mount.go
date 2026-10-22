//go:build linux || darwin

// Package fusefs translates the provider session into a read-only FUSE file
// system. It contains no provider-specific business logic.
package fusefs

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

type fileHandle struct {
	reader provider.Reader
}

func (h *fileHandle) Release(context.Context) syscall.Errno {
	err := h.reader.Close()
	h.reader = nil
	return errno(err)
}

type node struct {
	fs.Inode
	session  provider.Session
	object   provider.Object
	attrTTL  time.Duration
	entryTTL time.Duration
}

var (
	_ fs.NodeGetattrer = (*node)(nil)
	_ fs.NodeLookuper  = (*node)(nil)
	_ fs.NodeReaddirer = (*node)(nil)
	_ fs.NodeOpener    = (*node)(nil)
	_ fs.NodeReader    = (*node)(nil)
)

func (n *node) Getattr(ctx context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	object, err := n.session.Stat(ctx, n.object)
	if err != nil {
		return errno(err)
	}
	fillAttr(&out.Attr, object)
	return 0
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if !provider.ValidName(name) {
		return nil, syscall.ENOENT
	}
	objects, err := n.session.List(ctx, n.object)
	if err != nil {
		return nil, errno(err)
	}
	for _, object := range objects {
		if object.Name == name {
			child := n.child(ctx, object)
			fillAttr(&out.Attr, object)
			out.SetEntryTimeout(n.entryTimeout(object))
			out.SetAttrTimeout(n.attrTimeout(object))
			return child, 0
		}
	}
	return nil, syscall.ENOENT
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	objects, err := n.session.List(ctx, n.object)
	if err != nil {
		return nil, errno(err)
	}
	entries := make([]fuse.DirEntry, 0, len(objects))
	for _, object := range objects {
		entries = append(entries, fuse.DirEntry{Name: object.Name, Mode: mode(object), Ino: inode(object.ID)})
	}
	return fs.NewListDirStream(entries), 0
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if flags&uint32(syscall.O_ACCMODE) != uint32(syscall.O_RDONLY) {
		return nil, 0, syscall.EROFS
	}
	if n.object.IsDir() {
		return nil, 0, syscall.EISDIR
	}
	reader, err := n.session.OpenRead(ctx, n.object)
	if err != nil {
		return nil, 0, errno(err)
	}
	return &fileHandle{reader: reader}, 0, 0
}

func (n *node) Read(ctx context.Context, handle fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h, ok := handle.(*fileHandle)
	if !ok || h.reader == nil {
		return nil, syscall.EBADF
	}
	nread, err := h.reader.ReadAt(ctx, dest, off)
	if nread < 0 || nread > len(dest) {
		return nil, syscall.EIO
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errno(err)
	}
	return fuse.ReadResultData(dest[:nread]), 0
}

func (n *node) child(ctx context.Context, object provider.Object) *fs.Inode {
	if old := n.GetChild(object.Name); old != nil {
		if existing, ok := old.Operations().(*node); ok && existing.object.ID == object.ID {
			return old
		}
	}
	stable := fs.StableAttr{Mode: mode(object), Ino: inode(object.ID)}
	return n.NewInode(ctx, &node{session: n.session, object: object, attrTTL: n.attrTTL, entryTTL: n.entryTTL}, stable)
}

func (n *node) attrTimeout(provider.Object) time.Duration  { return n.attrTTL }
func (n *node) entryTimeout(provider.Object) time.Duration { return n.entryTTL }

func mode(object provider.Object) uint32 {
	if object.IsDir() {
		return fuse.S_IFDIR | 0555
	}
	return fuse.S_IFREG | 0444
}

func inode(id string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	value := h.Sum64()
	if value == 0 {
		return 1
	}
	return value
}

func fillAttr(attr *fuse.Attr, object provider.Object) {
	attr.Mode = mode(object)
	attr.Nlink = 1
	attr.Uid, attr.Gid = uint32(os.Getuid()), uint32(os.Getgid())
	attr.Blksize = 4096
	if object.IsDir() {
		attr.Size = 4096
	} else {
		attr.Size = uint64(max(object.Size, 0))
		attr.Blocks = (attr.Size + 511) / 512
	}
	modified := object.Modified
	if modified.IsZero() {
		modified = time.Unix(0, 0)
	}
	attr.SetTimes(&modified, &modified, &modified)
}

func errno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	switch {
	case errors.Is(err, provider.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, provider.ErrPermission), errors.Is(err, os.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, provider.ErrNotDirectory):
		return syscall.ENOTDIR
	case errors.Is(err, provider.ErrConflict):
		return syscall.EBUSY
	case errors.Is(err, provider.ErrUnsupported):
		return syscall.ENOSYS
	case errors.Is(err, context.Canceled):
		return syscall.EINTR
	case errors.Is(err, context.DeadlineExceeded):
		return syscall.ETIMEDOUT
	}
	var code syscall.Errno
	if errors.As(err, &code) {
		return code
	}
	return syscall.EIO
}

// Mount serves session until ctx is cancelled or the driver stops. The caller
// owns session.Close and may reuse it after the mount returns.
func Mount(ctx context.Context, session provider.Session, mountpoint string, options Options) error {
	if session == nil {
		return errors.New("missing provider session")
	}
	if !options.ReadOnly {
		return errors.New("only read-only mounts are supported")
	}
	point, err := ValidateMountpoint(mountpoint)
	if err != nil {
		return err
	}
	rootObject, err := session.Root(ctx)
	if err != nil {
		return err
	}
	if !rootObject.IsDir() {
		return provider.ErrNotDirectory
	}
	attrTTL, entryTTL := options.AttrTTL, options.EntryTTL
	if attrTTL <= 0 {
		attrTTL = 2 * time.Second
	}
	if entryTTL <= 0 {
		entryTTL = 2 * time.Second
	}
	name := options.VolumeName
	if name == "" {
		name = "FuseDock"
	}
	mountOptions := &fs.Options{
		MountOptions: fuse.MountOptions{
			Name: name, FsName: name, Debug: options.Debug, DisableXAttrs: true,
			Options: []string{"ro"},
		},
		EntryTimeout: &entryTTL,
		AttrTimeout:  &attrTTL,
	}
	root := &node{session: session, object: rootObject, attrTTL: attrTTL, entryTTL: entryTTL}
	server, err := fs.Mount(point, root, mountOptions)
	if err != nil {
		return mountError(err)
	}
	done := make(chan struct{})
	go func() {
		server.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		if err := server.Unmount(); err != nil {
			return fmt.Errorf("unmount: %w", err)
		}
		<-done
		return nil
	case <-done:
		return nil
	}
}

func ValidateMountpoint(mountpoint string) (string, error) {
	if mountpoint == "" {
		return "", errors.New("mountpoint is required")
	}
	point, err := filepath.Abs(mountpoint)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(point)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", syscall.ENOTDIR
	}
	contents, err := os.ReadDir(point)
	if err != nil {
		return "", err
	}
	if len(contents) != 0 {
		return "", errors.New("mountpoint must be an empty directory")
	}
	return point, nil
}

func mountError(err error) error {
	message := err.Error()
	if strings.Contains(message, "no FUSE mount utility found") || strings.Contains(message, "/dev/fuse") {
		switch runtime.GOOS {
		case "darwin":
			return fmt.Errorf("FUSE mount: %w\ninstall macFUSE and enable its kernel extension", err)
		case "linux":
			return fmt.Errorf("FUSE mount: %w\ninstall FUSE 3 and verify that /dev/fuse is accessible", err)
		}
	}
	return fmt.Errorf("FUSE mount: %w", err)
}
