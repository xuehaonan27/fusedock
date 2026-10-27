//go:build linux

package fusefs

import (
	"context"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

type collisionRoot struct {
	fs.Inode
}

func (r *collisionRoot) Lookup(ctx context.Context, name string, _ *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	child := &node{
		session: &dummySession{},
		object: provider.Object{
			ID: "distinct-object-" + name, Name: name, Kind: provider.KindFile, Size: 1,
		},
	}
	// Two distinct objects deliberately share the same StableAttr.Ino.
	return r.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG, Ino: 424242}), 0
}

// TestGoFuseDeduplicatesSharedStableIno records the concrete failure mode of
// deriving a platform inode from a hash: go-fuse sees equal StableAttr and
// returns the already-known Inode. This is dependency evidence, not desired
// FUSE behavior. The NodeKey mapping must ensure distinct objects never reach
// this state; only a Linux FUSE run can complete the acceptance test.
func TestGoFuseDeduplicatesSharedStableIno(t *testing.T) {
	root := &collisionRoot{}
	raw := fs.NewNodeFS(root, &fs.Options{})
	header := &fuse.InHeader{NodeId: 1}
	var first, second fuse.EntryOut
	if status := raw.Lookup(nil, header, "first", &first); status != fuse.OK {
		t.Fatalf("first lookup status = %v", status)
	}
	if status := raw.Lookup(nil, header, "second", &second); status != fuse.OK {
		t.Fatalf("second lookup status = %v", status)
	}
	if first.NodeId != second.NodeId {
		t.Fatalf("go-fuse did not deduplicate shared StableAttr.Ino: %d != %d", first.NodeId, second.NodeId)
	}
}
