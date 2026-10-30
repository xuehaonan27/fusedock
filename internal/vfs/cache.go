// Package vfs owns filesystem read snapshots and shared metadata/data caches.
package vfs

import (
	"container/list"
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xuehaonan27/fusedock/internal/blockcache"
	"github.com/xuehaonan27/fusedock/internal/namespace"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

const metadataBudget = int64(8 << 20)

type metadataEntry struct {
	key     string
	objects []provider.Object
	expires time.Time
	cost    int64
	elem    *list.Element
}
type metadataFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	objects []provider.Object
	err     error
	epoch   uint64
}
type Counters struct{ List, Stat, Read, LatencyNanos, QueueNanos, Cancellations atomic.Uint64 }
type Session struct {
	provider.Session
	ttl                   time.Duration
	namespace             namespace.Namespace
	blocks                *blockcache.Cache
	ownsCache, persistent bool
	wholeFileLimit        int64
	ctx                   context.Context
	cancel                context.CancelFunc
	readersMu             sync.Mutex
	readers               map[*reader]struct{}
	closeOnce             sync.Once
	closeErr              error
	mu                    sync.Mutex
	metadata              map[string]*metadataEntry
	waiting               map[string]*metadataFlight
	recency               list.List
	metadataBytes         int64
	epoch                 uint64
	metadataSlots         chan struct{}
	metadataWorkers       sync.WaitGroup
	Counters              Counters
}

func New(session provider.Session, ttl time.Duration) *Session {
	id, err := namespace.NewAccountID()
	if err != nil {
		panic(err)
	}
	s, err := NewWithOptions(session, Options{Namespace: namespace.Namespace{AccountID: id, ProviderID: "session"}, MetadataTTL: ttl})
	if err != nil {
		panic(err)
	}
	return s
}
func (s *Session) Root(ctx context.Context) (provider.Object, error) {
	return s.Stat(ctx, provider.Object{})
}
func (s *Session) Stat(ctx context.Context, o provider.Object) (provider.Object, error) {
	key := "stat\x00" + o.ID
	objects, err := s.load(ctx, key, func(ctx context.Context) ([]provider.Object, error) {
		s.Counters.Stat.Add(1)
		var object provider.Object
		var err error
		if o.ID == "" {
			object, err = s.Session.Root(ctx)
		} else {
			object, err = s.Session.Stat(ctx, o)
		}
		return []provider.Object{object}, err
	})
	if err != nil {
		return provider.Object{}, err
	}
	return objects[0], nil
}
func (s *Session) List(ctx context.Context, parent provider.Object) ([]provider.Object, error) {
	return s.load(ctx, "list\x00"+parent.ID, func(ctx context.Context) ([]provider.Object, error) {
		s.Counters.List.Add(1)
		return s.Session.List(ctx, parent)
	})
}
func objectCost(o provider.Object) int64 {
	return int64(256 + len(o.ID) + len(o.ParentID) + len(o.Name) + len(o.Version) + len(o.ContentVersion) + len(o.MetadataVersion) + len(o.Digest.Value))
}
func (s *Session) load(ctx context.Context, key string, fetch func(context.Context) ([]provider.Object, error)) ([]provider.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if err := s.ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if e := s.metadata[key]; e != nil && time.Now().Before(e.expires) {
		s.recency.MoveToFront(e.elem)
		result := append([]provider.Object(nil), e.objects...)
		s.mu.Unlock()
		return result, nil
	}
	f := s.waiting[key]
	if f == nil || f.epoch != s.epoch {
		fillCtx, cancel := context.WithCancel(s.ctx)
		f = &metadataFlight{done: make(chan struct{}), cancel: cancel, epoch: s.epoch}
		s.waiting[key] = f
		s.metadataWorkers.Add(1)
		go s.fillMetadata(fillCtx, key, f, fetch)
	}
	f.waiters++
	s.mu.Unlock()
	select {
	case <-f.done:
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f.waiters--
	if err := ctx.Err(); err != nil {
		s.Counters.Cancellations.Add(1)
		if f.waiters == 0 {
			f.cancel()
			if s.waiting[key] == f {
				delete(s.waiting, key)
			}
		}
		return nil, err
	}
	return append([]provider.Object(nil), f.objects...), f.err
}
func (s *Session) fillMetadata(ctx context.Context, key string, f *metadataFlight, fetch func(context.Context) ([]provider.Object, error)) {
	defer s.metadataWorkers.Done()
	defer f.cancel()
	queued := time.Now()
	var objects []provider.Object
	var err error
	select {
	case s.metadataSlots <- struct{}{}:
		s.Counters.QueueNanos.Add(uint64(time.Since(queued)))
		start := time.Now()
		objects, err = fetch(ctx)
		s.Counters.LatencyNanos.Add(uint64(time.Since(start)))
		<-s.metadataSlots
	case <-ctx.Done():
		err = ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f.objects, f.err = objects, err
	if s.waiting[key] == f {
		delete(s.waiting, key)
	}
	cost := int64(len(key) + 128)
	for _, o := range objects {
		cost += objectCost(o)
	}
	if err == nil && ctx.Err() == nil && s.ttl > 0 && f.epoch == s.epoch && cost <= metadataBudget {
		if old := s.metadata[key]; old != nil {
			s.removeMetadata(old)
		}
		for s.metadataBytes+cost > metadataBudget {
			s.removeMetadata(s.recency.Back().Value.(*metadataEntry))
		}
		e := &metadataEntry{key: key, objects: objects, expires: time.Now().Add(s.ttl), cost: cost}
		e.elem = s.recency.PushFront(e)
		s.metadata[key] = e
		s.metadataBytes += cost
	}
	close(f.done)
}
func (s *Session) removeMetadata(e *metadataEntry) {
	delete(s.metadata, e.key)
	s.recency.Remove(e.elem)
	s.metadataBytes -= e.cost
}
func (s *Session) Invalidate(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	if id == "" {
		clear(s.metadata)
		s.recency.Init()
		s.metadataBytes = 0
		return
	}
	for _, key := range []string{"stat\x00" + id, "list\x00" + id} {
		if e := s.metadata[key]; e != nil {
			s.removeMetadata(e)
		}
	}
}
