package vfs

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/xuehaonan27/fusedock/internal/blockcache"
	"github.com/xuehaonan27/fusedock/internal/namespace"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"sync"
	"sync/atomic"
	"testing"
)

type revisionSession struct {
	fakeSession
	mu      sync.Mutex
	data    map[string][]byte
	caps    provider.ReadCapabilities
	current []byte
	opens   atomic.Int32
}

func (s *revisionSession) ReadCapabilities(context.Context) (provider.ReadCapabilities, error) {
	return s.caps, nil
}
func (s *revisionSession) OpenReadRevision(_ context.Context, r provider.ObjectRevision) (provider.Reader, error) {
	s.opens.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data[r.ContentVersion]
	if !ok {
		return nil, provider.ErrConflict
	}
	return &rangeReader{data: b}, nil
}
func (s *revisionSession) OpenRead(context.Context, provider.Object) (provider.Reader, error) {
	s.opens.Add(1)
	return &rangeReader{data: s.current}, nil
}
func TestImmutableRevisionsAndConcurrentSharing(t *testing.T) {
	for _, readers := range []int{1, 10, 100, 1000} {
		t.Run(fmt.Sprint(readers), func(t *testing.T) {
			cache, err := blockcache.New(blockcache.Options{MemoryBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			defer cache.Close()
			id, _ := namespace.NewAccountID()
			base := &revisionSession{data: map[string][]byte{"a": []byte("old"), "b": []byte("new")}, caps: provider.ReadCapabilities{StableObjectID: true, ExactRange: true}}
			s, err := NewWithOptions(base, Options{Namespace: namespace.Namespace{AccountID: id, ProviderID: "fixture"}, Cache: cache, Persistent: true})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			old := provider.Object{ID: "same", Name: "same", Kind: provider.KindFile, Size: 3, ContentVersion: "a"}
			next := old
			next.ContentVersion = "b"
			var wg sync.WaitGroup
			for i := 0; i < readers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r, err := s.OpenRead(context.Background(), old)
					if err != nil {
						t.Error(err)
						return
					}
					defer r.Close()
					b := make([]byte, 3)
					if _, err := r.ReadAt(context.Background(), b, 0); err != nil || string(b) != "old" {
						t.Errorf("old read: %q %v", b, err)
					}
				}()
			}
			wg.Wait()
			r, err := s.OpenRead(context.Background(), next)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			b := make([]byte, 3)
			if _, err := r.ReadAt(context.Background(), b, 0); err != nil || string(b) != "new" {
				t.Fatalf("new read: %q %v", b, err)
			}
			if base.opens.Load() != 2 {
				t.Fatalf("shared ranges fetched %d times", base.opens.Load())
			}
			if cache.Stats().MemoryBytes > 1024 {
				t.Fatal(cache.Stats())
			}
		})
	}
}
func TestWholeObjectDigestBeforeDelivery(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			bytes := []byte("original bytes")
			hash := md5.Sum(bytes)
			digest := hex.EncodeToString(hash[:])
			base := &revisionSession{current: bytes, caps: provider.ReadCapabilities{StableObjectID: true, DeclaredDigest: true}}
			if mismatch {
				base.current = []byte("modified bytes")
			}
			s := New(base, 0)
			defer s.Close()
			object := provider.Object{ID: "f", Kind: provider.KindFile, Size: int64(len(bytes)), ContentVersion: "md5:" + digest, Digest: provider.Digest{Algorithm: "md5", Value: digest}}
			r, err := s.OpenRead(context.Background(), object)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			dest := []byte{99}
			n, err := r.ReadAt(context.Background(), dest, 4)
			if mismatch {
				if !errors.Is(err, provider.ErrConflict) || n != 0 || dest[0] != 99 || s.CacheStats().Entries != 0 {
					t.Fatalf("unverified bytes delivered: %d %v %+v", n, err, s.CacheStats())
				}
			} else if err != nil || n != 1 || dest[0] != bytes[4] {
				t.Fatalf("verified read: %d %v", n, err)
			}
		})
	}
}
func TestWeakReadCloseReleasesBlocks(t *testing.T) {
	s := New(&readSession{reader: &rangeReader{data: make([]byte, 1024)}}, 0)
	defer s.Close()
	for i := 0; i < 100; i++ {
		r, err := s.OpenRead(context.Background(), provider.Object{ID: fmt.Sprint(i), Kind: provider.KindFile, Size: 1024})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.ReadAt(context.Background(), make([]byte, 1), 0); err != nil {
			t.Fatal(err)
		}
		r.Close()
	}
	if s.CacheStats().MemoryBytes != 0 || len(s.readers) != 0 {
		t.Fatal("closed readers retained data")
	}
}
