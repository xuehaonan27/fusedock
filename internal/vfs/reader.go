package vfs

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xuehaonan27/fusedock/internal/blockcache"
	"github.com/xuehaonan27/fusedock/internal/namespace"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

const defaultReadBlock = int64(4 << 20)

var nextAccess atomic.Uint64

type reader struct {
	session               *Session
	snapshot              provider.Object
	remote                provider.Reader
	exact, whole, persist bool
	access                uint64
	scope                 string
	block                 int64
	mu                    sync.RWMutex
	closed                bool
	ctx                   context.Context
	cancel                context.CancelFunc
}

func (s *Session) OpenRead(ctx context.Context, object provider.Object) (provider.Reader, error) {
	if object.IsDir() {
		return nil, provider.ErrNotDirectory
	}
	if object.Size < 0 {
		return nil, errors.New("cannot read an object with unknown size")
	}
	caps, err := provider.CapabilitiesFor(ctx, s.Session)
	if err != nil {
		return nil, err
	}
	_, revisionReader := s.Session.(provider.RevisionReader)
	exact := caps.ExactRange && revisionReader && object.ContentVersion != ""
	whole := !exact && caps.DeclaredDigest && object.Digest.Valid() && object.ContentVersion != "" && object.Size <= s.wholeFileLimit
	r := &reader{session: s, snapshot: object, exact: exact, whole: whole, access: nextAccess.Add(1), block: min(defaultReadBlock, s.blocks.Limit())}
	r.persist = (exact || whole) && caps.StableObjectID && s.persistent && !object.NoCache
	if !exact && !whole {
		r.scope = fmt.Sprint(r.access)
	}
	if whole {
		r.block = max(object.Size, 1)
	}
	r.ctx, r.cancel = context.WithCancel(s.ctx)
	if !exact {
		r.remote, err = s.Session.OpenRead(ctx, object)
		if err != nil {
			r.cancel()
			return nil, err
		}
		if snapshot, ok := r.remote.(provider.SnapshotReader); ok {
			if whole && snapshot.Snapshot().Revision() != object.Revision() {
				r.remote.Close()
				r.cancel()
				return nil, provider.ErrConflict
			}
			r.snapshot = snapshot.Snapshot()
		}
	}
	s.readersMu.Lock()
	if s.ctx.Err() != nil {
		s.readersMu.Unlock()
		r.cancel()
		if r.remote != nil {
			r.remote.Close()
		}
		return nil, errors.New("session closed")
	}
	s.readers[r] = struct{}{}
	s.readersMu.Unlock()
	return r, nil
}
func (r *reader) Snapshot() provider.Object { return r.snapshot }
func (r *reader) ReadAt(ctx context.Context, dest []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative read offset")
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	defer cancel()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(dest) == 0 || off >= r.snapshot.Size {
		return 0, nil
	}
	written := 0
	for written < len(dest) && off < r.snapshot.Size {
		start := off / r.block * r.block
		length := min(r.block, r.snapshot.Size-start)
		key := blockcache.Key{Node: namespace.NodeKey{Namespace: r.session.namespace, ObjectID: r.snapshot.ID}, Version: r.snapshot.ContentVersion, Size: r.snapshot.Size, Offset: start, Length: length, Scope: r.scope}
		lease, err := r.session.blocks.Acquire(ctx, key, r.persist, r.access, func(fillCtx context.Context, b []byte) error { return r.fill(fillCtx, b, start) })
		if err != nil {
			return written, err
		}
		n := copy(dest[written:], lease.Bytes()[off-start:])
		lease.Release()
		written += n
		off += int64(n)
	}
	return written, nil
}
func (r *reader) fill(ctx context.Context, b []byte, off int64) error {
	r.session.Counters.Read.Add(1)
	start := time.Now()
	defer func() { r.session.Counters.LatencyNanos.Add(uint64(time.Since(start))) }()
	var source provider.Reader
	if r.exact {
		var err error
		source, err = r.session.Session.(provider.RevisionReader).OpenReadRevision(ctx, r.snapshot.Revision())
		if err != nil {
			return err
		}
		defer source.Close()
	} else {
		r.mu.RLock()
		defer r.mu.RUnlock()
		if r.closed {
			return errors.New("read handle closed")
		}
		source = r.remote
	}
	n, err := source.ReadAt(ctx, b, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n != len(b) {
		return io.ErrUnexpectedEOF
	}
	if r.whole {
		hash, _ := r.snapshot.Digest.Hash()
		hash.Write(b)
		if hex.EncodeToString(hash.Sum(nil)) != r.snapshot.Digest.Value {
			return provider.ErrConflict
		}
	}
	return nil
}
func (r *reader) Close() error {
	r.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	r.session.readersMu.Lock()
	delete(r.session.readers, r)
	r.session.readersMu.Unlock()
	if r.scope != "" {
		r.session.blocks.DiscardScope(r.session.namespace, r.scope)
	}
	if r.remote != nil {
		return r.remote.Close()
	}
	return nil
}
