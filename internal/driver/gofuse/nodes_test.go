//go:build linux || darwin

package gofuse

import (
	"context"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"syscall"
	"testing"
)

type duplicateRoot struct{ fs.Inode }

func (n *duplicateRoot) Lookup(ctx context.Context, _ string, _ *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return n.NewInode(ctx, &fs.Inode{}, fs.StableAttr{Mode: fuse.S_IFREG, Ino: 424242}), 0
}
func TestEqualStableAttrDeduplicates(t *testing.T) {
	raw := fs.NewNodeFS(&duplicateRoot{}, &fs.Options{})
	var a, b fuse.EntryOut
	input := fuse.InHeader{NodeId: 1}
	if raw.Lookup(nil, &input, "a", &a) != fuse.OK || raw.Lookup(nil, &input, "b", &b) != fuse.OK {
		t.Fatal("lookup failed")
	}
	if a.NodeId != b.NodeId || a.Ino != 424242 {
		t.Fatalf("unexpected deduplication: %d %d %d", a.NodeId, b.NodeId, a.Ino)
	}
}

type librarySession struct {
	provider.Session
	objects []provider.Object
}

func (s *librarySession) List(context.Context, provider.Object) ([]provider.Object, error) {
	return s.objects, nil
}
func TestAllocatorAndRevisionGeneration(t *testing.T) {
	s := &librarySession{objects: []provider.Object{{ID: "a", Name: "a", Kind: provider.KindFile, Size: 3, ContentVersion: "v1"}, {ID: "b", Name: "b", Kind: provider.KindFile, Size: 3, ContentVersion: "v1"}}}
	raw := fs.NewNodeFS(NewRoot(s, provider.Object{ID: "root", Kind: provider.KindDirectory}, 0, 0), &fs.Options{})
	input := fuse.InHeader{NodeId: 1}
	var a, b, newA fuse.EntryOut
	if raw.Lookup(nil, &input, "a", &a) != fuse.OK || raw.Lookup(nil, &input, "b", &b) != fuse.OK {
		t.Fatal("lookup failed")
	}
	if a.NodeId == b.NodeId || a.Ino == b.Ino {
		t.Fatal("distinct objects aliased")
	}
	s.objects[0].ContentVersion = "v2"
	if raw.Lookup(nil, &input, "a", &newA) != fuse.OK {
		t.Fatal("new revision lookup")
	}
	if a.Ino != newA.Ino || a.NodeId == newA.NodeId || a.Generation == newA.Generation {
		t.Fatalf("revision identity old=%+v new=%+v", a, newA)
	}
	raw.Forget(a.NodeId, 1)
	raw.Forget(b.NodeId, 1)
	raw.Forget(newA.NodeId, 1)
}
