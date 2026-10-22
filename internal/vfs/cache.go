// Package vfs provides driver-independent caching over a provider session.
// Mutations and write-back deliberately live outside this package until the
// provider contract has a common revision-conflict model.
package vfs

import (
	"context"
	"sync"
	"time"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

const maxCacheEntries = 4096

type cachedList struct {
	expires time.Time
	objects []provider.Object
}

type listFlight struct {
	done    chan struct{}
	objects []provider.Object
	err     error
}

type cachedStat struct {
	expires time.Time
	object  provider.Object
}

type statFlight struct {
	done   chan struct{}
	object provider.Object
	err    error
}

// Session coalesces concurrent metadata requests and retains short-lived
// snapshots. Read handles always go directly to the provider.
type Session struct {
	provider.Session
	ttl time.Duration

	mu       sync.Mutex
	lists    map[string]cachedList
	listWait map[string]*listFlight
	stats    map[string]cachedStat
	statWait map[string]*statFlight
}

func New(session provider.Session, ttl time.Duration) *Session {
	if ttl < 0 {
		ttl = 0
	}
	return &Session{
		Session: session, ttl: ttl,
		lists: make(map[string]cachedList), listWait: make(map[string]*listFlight),
		stats: make(map[string]cachedStat), statWait: make(map[string]*statFlight),
	}
}

func (s *Session) Root(ctx context.Context) (provider.Object, error) {
	return s.stat(ctx, provider.Object{}, true)
}

func (s *Session) List(ctx context.Context, parent provider.Object) ([]provider.Object, error) {
	if s.ttl == 0 {
		return s.Session.List(ctx, parent)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if item, ok := s.lists[parent.ID]; ok && time.Now().Before(item.expires) {
		result := append([]provider.Object(nil), item.objects...)
		s.mu.Unlock()
		return result, nil
	}
	if flight := s.listWait[parent.ID]; flight != nil {
		s.mu.Unlock()
		select {
		case <-flight.done:
			return append([]provider.Object(nil), flight.objects...), flight.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	flight := &listFlight{done: make(chan struct{})}
	s.listWait[parent.ID] = flight
	s.mu.Unlock()

	objects, err := s.Session.List(ctx, parent)
	result := append([]provider.Object(nil), objects...)
	s.mu.Lock()
	flight.objects, flight.err = result, err
	delete(s.listWait, parent.ID)
	if err == nil {
		if len(s.lists) >= maxCacheEntries {
			clear(s.lists)
		}
		s.lists[parent.ID] = cachedList{expires: time.Now().Add(s.ttl), objects: append([]provider.Object(nil), result...)}
	}
	close(flight.done)
	s.mu.Unlock()
	return result, err
}

func (s *Session) Stat(ctx context.Context, object provider.Object) (provider.Object, error) {
	return s.stat(ctx, object, object.ID == "")
}

func (s *Session) stat(ctx context.Context, object provider.Object, root bool) (provider.Object, error) {
	if s.ttl == 0 {
		if root {
			return s.Session.Root(ctx)
		}
		return s.Session.Stat(ctx, object)
	}
	if err := ctx.Err(); err != nil {
		return provider.Object{}, err
	}
	key := object.ID
	if key == "" {
		key = "\x00root"
	}
	s.mu.Lock()
	if item, ok := s.stats[key]; ok && time.Now().Before(item.expires) {
		s.mu.Unlock()
		return item.object, nil
	}
	if flight := s.statWait[key]; flight != nil {
		s.mu.Unlock()
		select {
		case <-flight.done:
			return flight.object, flight.err
		case <-ctx.Done():
			return provider.Object{}, ctx.Err()
		}
	}
	flight := &statFlight{done: make(chan struct{})}
	s.statWait[key] = flight
	s.mu.Unlock()

	var result provider.Object
	var err error
	if root {
		result, err = s.Session.Root(ctx)
	} else {
		result, err = s.Session.Stat(ctx, object)
	}
	s.mu.Lock()
	flight.object, flight.err = result, err
	delete(s.statWait, key)
	if err == nil {
		if len(s.stats) >= maxCacheEntries {
			clear(s.stats)
		}
		s.stats[key] = cachedStat{expires: time.Now().Add(s.ttl), object: result}
	}
	close(flight.done)
	s.mu.Unlock()
	return result, err
}
