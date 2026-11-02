package vfs

import (
	"context"
	"sync"
)

const initialReadAhead = int64(64 << 10)

type readAhead struct {
	mu                            sync.Mutex
	seen                          bool
	end, window, target, consumed int64
	cancel                        context.CancelFunc
	done                          chan struct{}
	workers                       sync.WaitGroup
}

func (r *reader) observeRead(off, end int64) bool {
	a := &r.ahead
	a.mu.Lock()
	defer a.mu.Unlock()
	sequential := a.seen && off == a.end
	if sequential {
		a.consumed += end - off
		if a.window == 0 {
			a.window = min(defaultReadBlock, max(initialReadAhead, end-off))
		} else {
			// Grow with bytes consumed, not callback count: repeated tiny
			// header reads keep a small window. Four times consumption gives
			// sequential streams enough lead to amortize request latency.
			a.window = max(initialReadAhead, min(a.consumed, defaultReadBlock/4)*4)
		}
	} else {
		a.window = 0
		a.consumed = 0
		if a.cancel != nil {
			a.cancel()
			a.cancel = nil
		}
	}
	a.seen, a.end = true, end
	return sequential
}

func (r *reader) prefetch(end int64) {
	a := &r.ahead
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.ctx.Err() != nil || end != a.end || end >= r.snapshot.Size {
		return
	}
	a.target = min(r.snapshot.Size, end+a.window)
	if a.done != nil {
		select {
		case <-a.done:
		default:
			return
		}
	}
	ctx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	a.cancel, a.done = cancel, done
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		defer close(done)
		defer cancel()
		for off := end; ctx.Err() == nil; {
			a.mu.Lock()
			limit := a.target
			a.mu.Unlock()
			if off >= limit {
				return
			}
			start := off / r.block * r.block
			length := min(r.block, r.snapshot.Size-start)
			n := min(limit-off, start+length-off)
			r.session.blocks.Prefetch(ctx, r.key(start, length), r.persist, r.access, off-start, n, r.prefetchFill)
			off += n
		}
	}()
}

func (r *reader) stopPrefetch() {
	a := &r.ahead
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Unlock()
	a.workers.Wait()
}
