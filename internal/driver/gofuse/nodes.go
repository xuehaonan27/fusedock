//go:build linux || darwin

// Package gofuse translates filesystem contracts without mounting a device.
package gofuse

import (
	"context"
	"errors"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/xuehaonan27/fusedock/internal/namespace"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"io"
	"os"
	"syscall"
	"time"
)

type fileHandle struct {
	reader   provider.Reader
	snapshot provider.Object
}

func (h *fileHandle) Release(context.Context) syscall.Errno {
	err := h.reader.Close()
	h.reader = nil
	return errno(err)
}

type node struct {
	fs.Inode
	session     provider.Session
	object      provider.Object
	attrTTL     time.Duration
	entryTTL    time.Duration
	ids         *namespace.Nodes
	generations *namespace.Nodes
	namespace   namespace.Namespace
}

var (
	_ fs.NodeGetattrer = (*node)(nil)
	_ fs.NodeLookuper  = (*node)(nil)
	_ fs.NodeReaddirer = (*node)(nil)
	_ fs.NodeOpener    = (*node)(nil)
	_ fs.NodeReader    = (*node)(nil)
)

func (n *node) Getattr(ctx context.Context, handle fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if h, ok := handle.(*fileHandle); ok {
		fillAttr(&out.Attr, h.snapshot)
		out.Ino = n.stableID(h.snapshot)
		return 0
	}
	if n.object.ContentVersion != "" && !n.object.IsDir() {
		fillAttr(&out.Attr, n.object)
		out.Ino = n.stableID(n.object)
		return 0
	}
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
		entries = append(entries, fuse.DirEntry{Name: object.Name, Mode: mode(object), Ino: n.stableID(object)})
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
	caps, err := provider.CapabilitiesFor(ctx, n.session)
	if err != nil {
		return nil, 0, errno(err)
	}
	var reader provider.Reader
	if exact, ok := n.session.(provider.RevisionReader); ok && caps.ExactRange && n.object.ContentVersion != "" {
		reader, err = exact.OpenReadRevision(ctx, n.object.Revision())
	} else {
		reader, err = n.session.OpenRead(ctx, n.object)
	}
	if err != nil {
		return nil, 0, errno(err)
	}
	snapshot := n.object
	if sr, ok := reader.(provider.SnapshotReader); ok && !caps.ExactRange {
		snapshot = sr.Snapshot()
	}
	return &fileHandle{reader: reader, snapshot: snapshot}, 0, 0
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
		if existing, ok := old.Operations().(*node); ok && existing.object.ID == object.ID && existing.object.ContentVersion == object.ContentVersion && existing.object.Version == object.Version {
			return old
		}
	}
	stable := fs.StableAttr{Mode: mode(object), Ino: n.stableID(object), Gen: n.generations.ID(namespace.NodeKey{Namespace: n.namespace, ObjectID: string(namespace.Encode(object.ID, object.ContentVersion, object.Version))})}
	return n.NewInode(ctx, &node{session: n.session, object: object, attrTTL: n.attrTTL, entryTTL: n.entryTTL, ids: n.ids, generations: n.generations, namespace: n.namespace}, stable)
}

func (n *node) attrTimeout(provider.Object) time.Duration  { return n.attrTTL }
func (n *node) entryTimeout(provider.Object) time.Duration { return n.entryTTL }

func mode(object provider.Object) uint32 {
	if object.IsDir() {
		return fuse.S_IFDIR | 0555
	}
	return fuse.S_IFREG | 0444
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
		return syscall.ESTALE
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

func (n *node) stableID(o provider.Object) uint64 {
	return n.ids.ID(namespace.NodeKey{Namespace: n.namespace, ObjectID: o.ID})
}
func NewRoot(session provider.Session, root provider.Object, attrTTL, entryTTL time.Duration) fs.InodeEmbedder {
	ns := namespace.Namespace{ProviderID: "mount"}
	if s, ok := session.(interface{ Namespace() namespace.Namespace }); ok {
		ns = s.Namespace()
	}
	return &node{session: session, object: root, attrTTL: attrTTL, entryTTL: entryTTL, ids: &namespace.Nodes{}, generations: &namespace.Nodes{}, namespace: ns}
}
