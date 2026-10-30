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
}
type flight struct {
	key     Key
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	waiters int
	entry   *entry
	err     error
	epoch   uint64
}
type Cache struct {
	mu                      sync.Mutex
	limit, used, reserved   int64
	recentBytes             int64
	entries                 map[string]*entry
	flights                 map[string]*flight
	recent, frequent, ghost list.List
	ghosts                  map[string]*list.Element
	changed                 chan struct{}
	slots                   chan struct{}
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
	c := &Cache{limit: o.MemoryBytes, entries: map[string]*entry{}, flights: map[string]*flight{}, ghosts: map[string]*list.Element{}, changed: make(chan struct{}), slots: make(chan struct{}, o.FillConcurrency), epochs: map[string]uint64{}}
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
	once  sync.Once
}

func (l *Lease) Bytes() []byte { return l.entry.data }
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

// Acquire coalesces fills without borrowing the first waiter's cancellation.
// fill must produce exactly key.Length verified bytes, or an error.
func (c *Cache) Acquire(ctx context.Context, k Key, persist bool, access uint64, fill func(context.Context, []byte) error) (*Lease, error) {
	if !k.Valid() || k.Length > c.limit {
		return nil, errors.New("block exceeds cache memory budget")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := k.Hash()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("cache closed")
	}
	if e := c.entries[id]; e != nil && e.retain {
		e.refs++
		c.touch(e, access)
		if persist && c.disk != nil {
			c.disk.touch(k, access)
		}
		c.Metrics.Hits.Add(1)
		c.mu.Unlock()
		return &Lease{cache: c, entry: e}, nil
	}
	f := c.flights[id]
	if f == nil {
		fctx, cancel := context.WithCancel(context.Background())
		f = &flight{key: k, done: make(chan struct{}), ctx: fctx, cancel: cancel, epoch: c.epochs[k.Node.Namespace.Tag()]}
		c.flights[id] = f
		c.workers.Add(1)
		c.Metrics.Misses.Add(1)
		go c.fetch(k, persist, access, f, fill)
	} else {
		c.Metrics.Coalesced.Add(1)
	}
	f.waiters++
	c.mu.Unlock()
	select {
	case <-f.done:
	case <-ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		f.waiters--
		if f.entry != nil {
			c.release(f.entry)
		}
		if f.waiters == 0 {
			f.cancel()
			if c.flights[id] == f {
				delete(c.flights, id)
			}
		}
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return &Lease{cache: c, entry: f.entry}, nil
}
func (c *Cache) fetch(k Key, persist bool, access uint64, f *flight, fill func(context.Context, []byte) error) {
	defer c.workers.Done()
	defer f.cancel()
	var data []byte
	var err error
	reserved := false
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-f.ctx.Done():
		err = f.ctx.Err()
	}
	if err == nil {
		c.mu.Lock()
		for c.used+c.reserved+k.Length > c.limit && f.ctx.Err() == nil && !c.closed {
			if c.evict() {
				continue
			}
			ch := c.changed
			c.mu.Unlock()
			select {
			case <-ch:
			case <-f.ctx.Done():
			}
			c.mu.Lock()
		}
		if c.closed {
			err = errors.New("cache closed")
		} else if f.ctx.Err() != nil {
			err = f.ctx.Err()
		} else {
			c.reserved += k.Length
			reserved = true
		}
		c.mu.Unlock()
	}
	if err == nil {
		data = make([]byte, int(k.Length))
		hit := false
		if persist && k.Scope == "" && k.Version != "" && c.disk != nil {
			hit = c.disk.read(k, data, access)
		}
		if !hit {
			err = fill(f.ctx, data)
			if err == nil {
				c.Metrics.NetworkBytes.Add(uint64(len(data)))
			}
		}
		if err == nil && !hit && persist && k.Scope == "" && k.Version != "" && c.disk != nil {
			c.mu.Lock()
			valid := !c.closed && c.epochs[k.Node.Namespace.Tag()] == f.epoch && f.ctx.Err() == nil
			c.mu.Unlock()
			if valid {
				c.disk.write(k, data, f.epoch, access)
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if reserved {
		c.reserved -= k.Length
	}
	if c.flights[k.Hash()] == f {
		delete(c.flights, k.Hash())
	}
	if err == nil && (c.closed || f.waiters == 0 || f.ctx.Err() != nil) {
		err = context.Canceled
	}
	if err == nil {
		e := &entry{key: k, data: data, refs: f.waiters, access: access, retain: c.epochs[k.Node.Namespace.Tag()] == f.epoch}
		if old := c.ghosts[k.Hash()]; old != nil {
			e.protected = true
			c.ghost.Remove(old)
			delete(c.ghosts, k.Hash())
		}
		if e.protected {
			e.elem = c.frequent.PushFront(e)
		} else {
			e.elem = c.recent.PushFront(e)
			c.recentBytes += k.Length
		}
		c.entries[k.Hash()] = e
		c.used += k.Length
		f.entry = e
	}
	f.err = err
	close(f.done)
	c.signal()
}
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	s := Stats{MemoryBytes: c.used, ReservedBytes: c.reserved, MemoryLimit: c.limit, Entries: len(c.entries), Flights: len(c.flights)}
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
	for _, f := range c.flights {
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
	for _, f := range c.flights {
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
