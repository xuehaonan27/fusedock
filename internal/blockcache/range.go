package blockcache

import (
	"context"
	"errors"
)

type span struct{ start, end int64 }

// RangeFill writes only dest. publish(n) makes its first n bytes immutable and
// available to waiting reads. It may be omitted when validation needs the whole
// response. offset is absolute within the remote object.
type RangeFill func(ctx context.Context, dest []byte, offset int64, publish func(int)) error

// Acquire requires the entire block, including whole-object proof validation.
func (c *Cache) Acquire(ctx context.Context, k Key, persist bool, access uint64, fill func(context.Context, []byte) error) (*Lease, error) {
	l, err := c.acquireRange(ctx, k, persist, access, 0, k.Length, true, func(ctx context.Context, b []byte, off int64, _ func(int)) error {
		return fill(ctx, b)
	})
	if err == nil {
		// The atomic API historically completes persistence before returning.
		c.mu.Lock()
		for len(l.entry.loads) != 0 && ctx.Err() == nil {
			changed := c.changed
			c.mu.Unlock()
			select {
			case <-changed:
			case <-ctx.Done():
			}
			c.mu.Lock()
		}
		stored := l.entry.stored
		touched := l.entry.touched
		c.mu.Unlock()
		for _, done := range []chan struct{}{stored, touched} {
			if done == nil {
				continue
			}
			select {
			case <-done:
			case <-ctx.Done():
				l.Release()
				return nil, ctx.Err()
			}
		}
	}
	return l, err
}

// AcquireRange returns only the requested slice of a block. Missing intervals
// are fetched independently; full blocks retain the existing disk key/format.
// The caller must only expose incrementally validated bytes through publish.
func (c *Cache) AcquireRange(ctx context.Context, k Key, persist bool, access uint64, offset, length int64, fill RangeFill) (*Lease, error) {
	return c.acquireRange(ctx, k, persist, access, offset, length, false, fill)
}

func (c *Cache) acquireRange(ctx context.Context, k Key, persist bool, access uint64, offset, length int64, atomic bool, fill RangeFill) (*Lease, error) {
	if !k.Valid() || k.Length > c.limit || offset < 0 || length <= 0 || offset > k.Length || length > k.Length-offset {
		return nil, errors.New("invalid cache range or memory budget")
	}
	e, err := c.pin(ctx, k, persist, access, atomic)
	if err != nil {
		return nil, err
	}
	want := span{offset, offset + length}
	c.mu.Lock()
	if covers(e.valid, want) {
		c.Metrics.Hits.Add(1)
	}
	for !covers(e.valid, want) {
		if err := ctx.Err(); err != nil {
			c.release(e)
			c.mu.Unlock()
			return nil, err
		}
		if c.closed {
			c.release(e)
			c.mu.Unlock()
			return nil, errors.New("cache closed")
		}
		missing := gap(e.valid, want)
		var f *flight
		for _, load := range e.loads {
			if load.span.start <= missing.start && load.span.end > missing.start {
				f = load
				break
			}
			if load.span.start > missing.start {
				missing.end = min(missing.end, load.span.start)
			}
		}
		if f == nil {
			f = c.start(e, missing)
			c.Metrics.Misses.Add(1)
			go c.fetchRange(f, fill)
		} else {
			c.Metrics.Coalesced.Add(1)
		}
		f.waiters++
		// A progressive fill can satisfy this caller before the rest of the
		// response, or another caller's larger range, has finished.
		for !covers(e.valid, want) && f.err == nil {
			select {
			case <-f.done:
				goto ready
			default:
			}
			changed := c.changed
			c.mu.Unlock()
			select {
			case <-changed:
			case <-ctx.Done():
			}
			c.mu.Lock()
			if ctx.Err() != nil {
				break
			}
		}
	ready:
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
		}
		err := ctx.Err()
		if err == nil && !covers(e.valid, want) {
			err = f.err
			// A previous last waiter cancelled this flight; a new live caller
			// retries the uncovered interval after its writer has stopped.
			if errors.Is(err, context.Canceled) && f.retry {
				err = nil
			}
		}
		if err != nil {
			if len(e.valid) == 0 && len(e.loads) == 0 {
				e.retain = false
			}
			c.release(e)
			c.mu.Unlock()
			return nil, err
		}
	}
	c.store(e, access)
	c.mu.Unlock()
	return &Lease{cache: c, entry: e, span: want}, nil
}

// Prefetch is best effort and uses at most half the global fill concurrency.
// It shares partial blocks and in-flight ranges with foreground readers.
func (c *Cache) Prefetch(ctx context.Context, k Key, persist bool, access uint64, offset, length int64, fill RangeFill) {
	select {
	case c.prefetch <- struct{}{}:
		defer func() { <-c.prefetch }()
	default:
		return
	}
	l, err := c.AcquireRange(ctx, k, persist, access, offset, length, fill)
	if err == nil {
		l.Release()
	}
}

func (c *Cache) pin(ctx context.Context, k Key, persist bool, access uint64, atomic bool) (*entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending++
	defer func() { c.pending-- }()
	id := k.Hash()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if c.closed {
			return nil, errors.New("cache closed")
		}
		if e := c.entries[id]; e != nil && e.retain {
			// Atomic fills (e.g. a whole-file digest proof) need one complete
			// response and cannot be invoked for a hole in a partial block.
			if atomic && !e.atomic && !covers(e.valid, span{0, k.Length}) {
				e.retain = false
				if e.refs == 0 {
					c.remove(e, false)
				}
				continue
			}
			e.refs++
			e.persist = e.persist || persist
			reused := e.access != access
			c.touch(e, access)
			if reused && persist && c.disk != nil {
				c.touchDisk(e)
			}
			return e, nil
		}
		if c.used+k.Length <= c.limit {
			break
		}
		if c.evict() {
			continue
		}
		ch := c.changed
		c.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		c.mu.Lock()
	}
	e := &entry{key: k, data: make([]byte, int(k.Length)), refs: 1, access: access, retain: true, epoch: c.epochs[k.Node.Namespace.Tag()], persist: persist, atomic: atomic}
	if old := c.ghosts[id]; old != nil {
		e.protected = true
		c.ghost.Remove(old)
		delete(c.ghosts, id)
	}
	if e.protected {
		e.elem = c.frequent.PushFront(e)
	} else {
		e.elem = c.recent.PushFront(e)
		c.recentBytes += k.Length
	}
	c.entries[id] = e
	c.used += k.Length
	if persist && k.Scope == "" && k.Version != "" && c.disk != nil {
		f := c.start(e, span{0, k.Length})
		go func() {
			defer c.workers.Done()
			hit := c.disk.read(k, e.data, access)
			c.mu.Lock()
			if hit {
				e.valid = []span{{0, k.Length}}
				e.stored = make(chan struct{})
				close(e.stored)
			}
			c.finish(f, nil)
			c.mu.Unlock()
		}()
	}
	return e, nil
}

func (c *Cache) start(e *entry, s span) *flight {
	ctx, cancel := context.WithCancel(context.Background())
	f := &flight{key: e.key, entry: e, span: s, done: make(chan struct{}), ctx: ctx, cancel: cancel}
	e.refs++
	e.loads = append(e.loads, f)
	c.flights[f] = struct{}{}
	c.workers.Add(1)
	return f
}

func (c *Cache) fetchRange(f *flight, fill RangeFill) {
	defer c.workers.Done()
	var err error
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-f.ctx.Done():
		err = f.ctx.Err()
	}
	published := 0
	publish := func(n int) {
		if int64(n) == f.span.end-f.span.start {
			// Final bytes and completion are published together below.
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if n > published {
			c.Metrics.NetworkBytes.Add(uint64(n - published))
			published = n
			f.entry.valid = merge(f.entry.valid, span{f.span.start, f.span.start + int64(n)})
			c.signal()
		}
	}
	if err == nil {
		b := f.entry.data[f.span.start:f.span.end]
		err = fill(f.ctx, b, f.key.Offset+f.span.start, publish)
	}
	c.mu.Lock()
	if err == nil {
		c.Metrics.NetworkBytes.Add(uint64(f.span.end - f.span.start - int64(published)))
		f.entry.valid = merge(f.entry.valid, f.span)
	}
	c.finish(f, err)
	c.mu.Unlock()
}

func (c *Cache) finish(f *flight, err error) {
	f.retry = errors.Is(err, context.Canceled) && f.ctx.Err() != nil
	f.cancel()
	f.err = err
	delete(c.flights, f)
	e := f.entry
	for i, load := range e.loads {
		if load == f {
			e.loads = append(e.loads[:i], e.loads[i+1:]...)
			break
		}
	}
	if err != nil && len(e.valid) == 0 && len(e.loads) == 0 && f.waiters == 0 {
		e.retain = false
	}
	c.store(e, e.access)
	close(f.done)
	c.release(e)
}

// Complete immutable blocks are written asynchronously and pinned until the
// write finishes. No partial interval is ever published under a disk key.
func (c *Cache) store(e *entry, access uint64) {
	if c.closed || !e.retain || !e.persist || e.stored != nil || c.disk == nil || e.key.Scope != "" || e.key.Version == "" || len(e.loads) != 0 || !covers(e.valid, span{0, e.key.Length}) {
		return
	}
	e.stored = make(chan struct{})
	e.refs++
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		c.mu.Lock()
		valid := c.epochs[e.key.Node.Namespace.Tag()] == e.epoch
		c.mu.Unlock()
		if valid {
			c.disk.write(e.key, e.data, e.epoch, access)
		}
		c.mu.Lock()
		close(e.stored)
		c.release(e)
		c.mu.Unlock()
	}()
}

// Recency updates must not make a memory hit wait for disk reads or writes.
// Coalesce updates per entry, including many simultaneous opens of one file.
func (c *Cache) touchDisk(e *entry) {
	if e.touched != nil {
		select {
		case <-e.touched:
		default:
			return
		}
	}
	done := make(chan struct{})
	e.touched = done
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		for {
			c.mu.Lock()
			access := e.access
			c.mu.Unlock()
			c.disk.touch(e.key, access)
			c.mu.Lock()
			finished := c.closed || access == e.access
			if finished {
				close(done)
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
		}
	}()
}

func covers(spans []span, want span) bool {
	for _, s := range spans {
		if s.start <= want.start && s.end >= want.end {
			return true
		}
	}
	return false
}

func gap(spans []span, want span) span {
	for _, s := range spans {
		if s.end <= want.start {
			continue
		}
		if s.start > want.start {
			want.end = min(want.end, s.start)
			break
		}
		want.start = s.end
	}
	return want
}

func merge(spans []span, add span) []span {
	i := 0
	for i < len(spans) && spans[i].end < add.start {
		i++
	}
	j := i
	for j < len(spans) && spans[j].start <= add.end {
		add.start = min(add.start, spans[j].start)
		add.end = max(add.end, spans[j].end)
		j++
	}
	result := append(spans[:i:i], add)
	return append(result, spans[j:]...)
}
