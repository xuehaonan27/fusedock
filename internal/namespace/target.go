package namespace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

// Target is a mount's resolved namespace. IDs are admitted by traversing its
// root; callers cannot supply metadata or read objects from another scope.
type Target struct {
	ctx    context.Context
	cancel context.CancelFunc
	provider.Session
	namespace  Namespace
	RootObject provider.Object
	Scope      string
	mu         sync.RWMutex
	known      map[string]provider.Object
	revoked    bool
}

func Resolve(ctx context.Context, s provider.Session, ns Namespace, scope string) (*Target, error) {
	if err := ns.Validate(); err != nil {
		return nil, err
	}
	root, err := s.Root(ctx)
	if err != nil {
		return nil, err
	}
	parts, err := provider.CleanRelativePath(scope)
	if err != nil {
		return nil, err
	}
	scope = "/" + strings.Join(parts, "/")
	for _, name := range parts {
		if name == "" {
			continue
		}
		if !provider.ValidName(name) {
			return nil, provider.ErrInvalidName
		}
		children, err := s.List(ctx, root)
		if err != nil {
			return nil, err
		}
		found := false
		for _, child := range children {
			if child.Name == name {
				root = child
				found = true
				break
			}
		}
		if !found {
			return nil, provider.ErrNotFound
		}
	}
	if !root.IsDir() {
		return nil, provider.ErrNotDirectory
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Target{ctx: ctx, cancel: cancel, Session: s, namespace: ns, RootObject: root, Scope: scope, known: map[string]provider.Object{root.ID: root}}, nil
}
func (t *Target) Namespace() Namespace { return t.namespace }
func (t *Target) VolumeID() string {
	h := sha256.Sum256(Encode("fusedock-volume-v1", string(t.namespace.AccountID), t.namespace.ProviderID, t.Scope))
	h[6] = h[6]&15 | 80
	h[8] = h[8]&63 | 128
	s := hex.EncodeToString(h[:16])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func (t *Target) Object(id string) (provider.Object, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.revoked {
		return provider.Object{}, provider.ErrPermission
	}
	o, ok := t.known[id]
	if !ok {
		return provider.Object{}, provider.ErrNotFound
	}
	return o, nil
}
func (t *Target) Root(ctx context.Context) (provider.Object, error) {
	if err := ctx.Err(); err != nil {
		return provider.Object{}, err
	}
	return t.Object(t.RootObject.ID)
}
func (t *Target) Stat(ctx context.Context, input provider.Object) (provider.Object, error) {
	o, err := t.Object(input.ID)
	if err != nil {
		return o, err
	}
	o, err = t.Session.Stat(ctx, o)
	if err != nil {
		return o, err
	}
	if o.ID != input.ID {
		return provider.Object{}, provider.ErrConflict
	}
	t.mu.Lock()
	if t.revoked {
		t.mu.Unlock()
		return provider.Object{}, provider.ErrPermission
	}
	t.known[o.ID] = o
	t.mu.Unlock()
	return o, nil
}
func (t *Target) List(ctx context.Context, input provider.Object) ([]provider.Object, error) {
	parent, err := t.Object(input.ID)
	if err != nil {
		return nil, err
	}
	if !parent.IsDir() {
		return nil, provider.ErrNotDirectory
	}
	objects, err := t.Session.List(ctx, parent)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(objects))
	ids := make(map[string]bool, len(objects))
	for _, o := range objects {
		if o.ID == "" || !provider.ValidName(o.Name) || names[o.Name] || ids[o.ID] {
			return nil, errors.New("directory contains an invalid or duplicate entry")
		}
		names[o.Name] = true
		ids[o.ID] = true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.revoked {
		return nil, provider.ErrPermission
	}
	for i := range objects {
		objects[i].ParentID = parent.ID
		o := objects[i]
		t.known[o.ID] = o
	}
	return objects, nil
}
func (t *Target) OpenRead(ctx context.Context, input provider.Object) (provider.Reader, error) {
	o, err := t.Stat(ctx, input)
	if err != nil {
		return nil, err
	}
	r, err := t.Session.OpenRead(ctx, o)
	if err != nil {
		return nil, err
	}
	if snapshot, ok := r.(provider.SnapshotReader); ok {
		o = snapshot.Snapshot()
	}
	return &targetReader{Reader: r, target: t, object: o}, nil
}
func (t *Target) Revoke()      { t.cancel(); t.mu.Lock(); t.revoked = true; clear(t.known); t.mu.Unlock() }
func (t *Target) Close() error { t.Revoke(); return nil }

type targetReader struct {
	provider.Reader
	target *Target
	object provider.Object
}

func (r *targetReader) Snapshot() provider.Object { return r.object }
func (r *targetReader) ReadAt(ctx context.Context, b []byte, off int64) (int, error) {
	if r.target.ctx.Err() != nil {
		return 0, provider.ErrPermission
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.target.ctx, cancel)
	defer stop()
	defer cancel()
	return r.Reader.ReadAt(ctx, b, off)
}

func (t *Target) ReadCapabilities(ctx context.Context) (provider.ReadCapabilities, error) {
	return provider.CapabilitiesFor(ctx, t.Session)
}
func (t *Target) OpenReadRevision(ctx context.Context, revision provider.ObjectRevision) (provider.Reader, error) {
	o, err := t.Object(revision.ObjectID)
	if err != nil {
		return nil, err
	}
	s, ok := t.Session.(provider.RevisionReader)
	if !ok {
		return nil, provider.ErrUnsupported
	}
	r, err := s.OpenReadRevision(ctx, revision)
	if err != nil {
		return nil, err
	}
	o.ContentVersion = revision.ContentVersion
	o.Size = revision.Size
	o.Digest = revision.Digest
	return &targetReader{Reader: r, target: t, object: o}, nil
}
