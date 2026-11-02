// Package blockcache shares bounded, verified read blocks across open handles.
package blockcache

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/xuehaonan27/fusedock/internal/namespace"
)

type Key struct {
	Node                 namespace.NodeKey
	Version              string
	Size, Offset, Length int64
	// Scope isolates unversioned readers in memory. Such keys are never persisted.
	Scope string
}

func (k Key) Hash() string {
	b := namespace.Encode("fusedock-block-v2", string(k.Node.Namespace.AccountID), k.Node.Namespace.ProviderID, k.Node.ObjectID, k.Version, k.Scope)
	for _, v := range []int64{k.Size, k.Offset, k.Length} {
		b = binary.BigEndian.AppendUint64(b, uint64(v))
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (k Key) Valid() bool {
	return k.Node.ObjectID != "" && k.Size >= 0 && k.Offset >= 0 && k.Length > 0 && k.Offset <= k.Size && k.Length <= k.Size-k.Offset
}

type Options struct {
	MemoryBytes     int64
	FillConcurrency int
	DiskDirectory   string
	DiskBytes       int64
}
type Metrics struct {
	Hits, Misses, Coalesced, NetworkBytes, DiskHits, Corrupt, WriteFailures atomic.Uint64
}
type Stats struct {
	MemoryBytes                                                             int64 `json:"memory_bytes"`
	ReservedBytes                                                           int64 `json:"reserved_bytes"`
	MemoryLimit                                                             int64 `json:"memory_limit"`
	Entries, Flights                                                        int
	Hits, Misses, Coalesced, NetworkBytes, DiskHits, Corrupt, WriteFailures uint64
	DiskBytes                                                               int64 `json:"disk_bytes"`
}
type entry struct {
	key       Key
	data      []byte
	refs      int
	protected bool
	elem      *list.Element
	access    uint64
	retain    bool
	valid     []span
	loads     []*flight
	epoch     uint64
	persist   bool
	stored    chan struct{}
	touched   chan struct{}
	atomic    bool
}
type flight struct {
	key     Key
	entry   *entry
	span    span
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	waiters int
	err     error
	retry   bool
}
type Cache struct {
	mu                      sync.Mutex
	limit, used, reserved   int64
	recentBytes             int64
	entries                 map[string]*entry
	flights                 map[*flight]struct{}
	pending                 int
	recent, frequent, ghost list.List
	ghosts                  map[string]*list.Element
	changed                 chan struct{}
	slots                   chan struct{}
	prefetch                chan struct{}
	closed                  bool
	workers                 sync.WaitGroup
	disk                    *diskStore
	epochs                  map[string]uint64
	Metrics                 Metrics
}

func New(o Options) (*Cache, error) {
	if o.MemoryBytes == 0 {
		o.MemoryBytes = 64 << 20
	}
	if o.FillConcurrency == 0 {
		o.FillConcurrency = 4
	}
	if o.MemoryBytes < 1 || o.FillConcurrency < 1 {
		return nil, errors.New("invalid cache budget")
	}
	c := &Cache{limit: o.MemoryBytes, entries: map[string]*entry{}, flights: map[*flight]struct{}{}, ghosts: map[string]*list.Element{}, changed: make(chan struct{}), slots: make(chan struct{}, o.FillConcurrency), prefetch: make(chan struct{}, o.FillConcurrency/2), epochs: map[string]uint64{}}
	if o.DiskDirectory != "" && o.DiskBytes > 0 {
		d, err := openDisk(o.DiskDirectory, o.DiskBytes, &c.Metrics)
		if err != nil {
			return nil, err
		}
		c.disk = d
	}
	return c, nil
}
func (c *Cache) Limit() int64 { return c.limit }
func (c *Cache) signal()      { close(c.changed); c.changed = make(chan struct{}) }

// Lease pins a block against eviction. Data is immutable until Release.
type Lease struct {
	cache *Cache
	entry *entry
	span  span
	once  sync.Once
}

func (l *Lease) Bytes() []byte { return l.entry.data[l.span.start:l.span.end] }
func (l *Lease) Release() {
	l.once.Do(func() { c := l.cache; c.mu.Lock(); c.release(l.entry); c.mu.Unlock() })
}
func (c *Cache) release(e *entry) {
	e.refs--
	if e.refs == 0 && !e.retain {
		c.remove(e, false)
	}
	c.signal()
}
func (c *Cache) remove(e *entry, remember bool) {
	id := e.key.Hash()
	if e.elem == nil {
		return
	}
	if c.entries[id] == e {
		delete(c.entries, id)
	}
	if e.protected {
		c.frequent.Remove(e.elem)
	} else {
		c.recent.Remove(e.elem)
		c.recentBytes -= int64(len(e.data))
	}
	c.used -= int64(len(e.data))
	e.data = nil
	e.elem = nil
	if remember && !e.protected {
		if old := c.ghosts[id]; old != nil {
			c.ghost.Remove(old)
		}
		c.ghosts[id] = c.ghost.PushFront(id)
	}
	for c.ghost.Len() > 4096 {
		last := c.ghost.Back()
		delete(c.ghosts, last.Value.(string))
		c.ghost.Remove(last)
	}
}
func (c *Cache) evict() bool {
	queues := []*list.List{&c.recent, &c.frequent}
	if c.recentBytes < max(1, c.limit/4) {
		queues = []*list.List{&c.frequent, &c.recent}
	}
	for _, q := range queues {
		for n := q.Back(); n != nil; n = n.Prev() {
			e := n.Value.(*entry)
			if e.refs == 0 {
				c.remove(e, true)
				return true
			}
		}
	}
	return false
}
func (c *Cache) touch(e *entry, access uint64) {
	// One open's correlated reads count once. A separate open/reuse can promote.
	if e.protected {
		c.frequent.MoveToFront(e.elem)
	} else if access != e.access {
		c.recent.Remove(e.elem)
		c.recentBytes -= int64(len(e.data))
		e.protected = true
		e.elem = c.frequent.PushFront(e)
	}
	e.access = access
}

func (c *Cache) Stats() Stats {
	c.mu.Lock()
	s := Stats{MemoryBytes: c.used, ReservedBytes: c.reserved, MemoryLimit: c.limit, Entries: len(c.entries), Flights: len(c.flights) + c.pending}
	c.mu.Unlock()
	m := &c.Metrics
	s.Hits = m.Hits.Load()
	s.Misses = m.Misses.Load()
	s.Coalesced = m.Coalesced.Load()
	s.NetworkBytes = m.NetworkBytes.Load()
	s.DiskHits = m.DiskHits.Load()
	s.Corrupt = m.Corrupt.Load()
	s.WriteFailures = m.WriteFailures.Load()
	if c.disk != nil {
		s.DiskBytes = c.disk.usage()
	}
	return s
}
func (c *Cache) Clear(ns namespace.Namespace) error {
	tag := ns.Tag()
	c.mu.Lock()
	c.epochs[tag]++
	for _, e := range c.entries {
		if e.key.Node.Namespace == ns {
			e.retain = false
			if e.refs == 0 {
				c.remove(e, false)
			}
		}
	}
	c.signal()
	epoch := c.epochs[tag]
	c.mu.Unlock()
	if c.disk != nil {
		return c.disk.clear(tag, epoch)
	}
	return nil
}
func (c *Cache) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	for _, e := range c.entries {
		e.retain = false
		if e.refs == 0 {
			c.remove(e, false)
		}
	}
	for f := range c.flights {
		f.cancel()
	}
	c.signal()
	c.mu.Unlock()
	c.workers.Wait()
	if c.disk != nil {
		return c.disk.close()
	}
	return nil
}

// DiscardScope releases unversioned per-open data at close.
func (c *Cache) DiscardScope(ns namespace.Namespace, scope string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for f := range c.flights {
		if f.key.Node.Namespace == ns && f.key.Scope == scope {
			f.cancel()
		}
	}
	for _, e := range c.entries {
		if e.key.Node.Namespace == ns && e.key.Scope == scope {
			e.retain = false
			if e.refs == 0 {
				c.remove(e, false)
			}
		}
	}
	c.signal()
}
