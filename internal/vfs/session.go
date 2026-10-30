package vfs

import (
	"context"
	"errors"
	"time"

	"github.com/xuehaonan27/fusedock/internal/blockcache"
	"github.com/xuehaonan27/fusedock/internal/namespace"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type Options struct {
	Namespace      namespace.Namespace
	Cache          *blockcache.Cache
	MetadataTTL    time.Duration
	Persistent     bool
	WholeFileLimit int64
	MetadataSlots  chan struct{}
}

func NewWithOptions(session provider.Session, o Options) (*Session, error) {
	if err := o.Namespace.Validate(); err != nil {
		return nil, err
	}
	owns := o.Cache == nil
	if owns {
		var err error
		o.Cache, err = blockcache.New(blockcache.Options{})
		if err != nil {
			return nil, err
		}
	}
	if o.WholeFileLimit == 0 {
		o.WholeFileLimit = min(16<<20, o.Cache.Limit()/4)
	}
	if o.WholeFileLimit < 0 || o.WholeFileLimit > o.Cache.Limit() {
		if owns {
			o.Cache.Close()
		}
		return nil, errors.New("whole-file limit exceeds memory budget")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if o.MetadataSlots == nil {
		o.MetadataSlots = make(chan struct{}, 4)
	}
	return &Session{Session: session, ttl: max(0, o.MetadataTTL), namespace: o.Namespace, blocks: o.Cache, ownsCache: owns, persistent: o.Persistent, wholeFileLimit: o.WholeFileLimit, ctx: ctx, cancel: cancel, readers: map[*reader]struct{}{}, metadata: map[string]*metadataEntry{}, waiting: map[string]*metadataFlight{}, metadataSlots: o.MetadataSlots}, nil
}
func (s *Session) Namespace() namespace.Namespace { return s.namespace }
func (s *Session) CacheStats() blockcache.Stats   { return s.blocks.Stats() }
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.cancel()
		s.mu.Unlock()
		s.metadataWorkers.Wait()
		s.readersMu.Lock()
		readers := make([]*reader, 0, len(s.readers))
		for r := range s.readers {
			readers = append(readers, r)
		}
		s.readersMu.Unlock()
		for _, r := range readers {
			s.closeErr = errors.Join(s.closeErr, r.Close())
		}
		if s.ownsCache {
			s.closeErr = errors.Join(s.closeErr, s.blocks.Close())
		}
		s.closeErr = errors.Join(s.closeErr, s.Session.Close())
	})
	return s.closeErr
}

func (s *Session) ReadCapabilities(ctx context.Context) (provider.ReadCapabilities, error) {
	return provider.CapabilitiesFor(ctx, s.Session)
}
func (s *Session) OpenReadRevision(ctx context.Context, r provider.ObjectRevision) (provider.Reader, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	caps, err := s.ReadCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	if !caps.ExactRange {
		return nil, provider.ErrUnsupported
	}
	return s.OpenRead(ctx, provider.Object{ID: r.ObjectID, Kind: provider.KindFile, ContentVersion: r.ContentVersion, Size: r.Size, Digest: r.Digest})
}
