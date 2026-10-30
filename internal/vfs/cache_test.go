package vfs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type fakeSession struct {
	listCalls atomic.Int32
	statCalls atomic.Int32
	gate      chan struct{}
}

func (f *fakeSession) Root(context.Context) (provider.Object, error) {
	return provider.Object{ID: "root", Name: "root", Kind: provider.KindDirectory}, nil
}
func (f *fakeSession) Stat(_ context.Context, object provider.Object) (provider.Object, error) {
	f.statCalls.Add(1)
	return object, nil
}
func (f *fakeSession) List(context.Context, provider.Object) ([]provider.Object, error) {
	f.listCalls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	return []provider.Object{{ID: "file", Name: "file", Kind: provider.KindFile, Size: 1}}, nil
}
func (f *fakeSession) OpenRead(context.Context, provider.Object) (provider.Reader, error) {
	return nil, provider.ErrUnsupported
}
func (f *fakeSession) Close() error { return nil }

func TestCachedSessionCoalescesAndExpires(t *testing.T) {
	base := &fakeSession{gate: make(chan struct{})}
	session := New(base, 50*time.Millisecond)
	root := provider.Object{ID: "root", Kind: provider.KindDirectory}
	const waiters = 8
	var wg sync.WaitGroup
	for range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			objects, err := session.List(context.Background(), root)
			if err != nil || len(objects) != 1 {
				t.Errorf("List=(%v,%v)", objects, err)
			}
		}()
	}
	time.Sleep(10 * time.Millisecond)
	close(base.gate)
	wg.Wait()
	if calls := base.listCalls.Load(); calls != 1 {
		t.Fatalf("coalesced requests made %d provider calls", calls)
	}
	if _, err := session.List(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if calls := base.listCalls.Load(); calls != 1 {
		t.Fatalf("fresh cache made %d provider calls", calls)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := session.List(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if calls := base.listCalls.Load(); calls != 2 {
		t.Fatalf("expired cache made %d provider calls", calls)
	}
}

func TestCachedSessionRootUsesRootOperation(t *testing.T) {
	base := &fakeSession{}
	session := New(base, time.Minute)
	root, err := session.Root(context.Background())
	if err != nil || root.ID != "root" {
		t.Fatalf("Root=(%+v,%v)", root, err)
	}
	if calls := base.statCalls.Load(); calls != 0 {
		t.Fatalf("Root used Stat %d times", calls)
	}
}

type refreshSession struct {
	fakeSession
	started chan struct{}
	finish  chan struct{}
}

func (s *refreshSession) List(ctx context.Context, _ provider.Object) ([]provider.Object, error) {
	name := "new"
	if s.listCalls.Add(1) == 1 {
		close(s.started)
		select {
		case <-s.finish:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		name = "old"
	}
	return []provider.Object{{ID: name, Name: name, Kind: provider.KindFile}}, nil
}

func TestRefreshDoesNotJoinOrRepublishOlderMetadata(t *testing.T) {
	base := &refreshSession{started: make(chan struct{}), finish: make(chan struct{})}
	s := New(base, time.Minute)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	root := provider.Object{ID: "root", Kind: provider.KindDirectory}
	old := make(chan error, 1)
	go func() { _, err := s.List(ctx, root); old <- err }()
	<-base.started
	s.Invalidate(root.ID)
	objects, err := s.List(ctx, root)
	if err != nil || len(objects) != 1 || objects[0].Name != "new" {
		t.Fatalf("refresh joined stale request: %v %v", objects, err)
	}
	close(base.finish)
	if err := <-old; err != nil {
		t.Fatal(err)
	}
	objects, err = s.List(ctx, root)
	if err != nil || len(objects) != 1 || objects[0].Name != "new" || base.listCalls.Load() != 2 {
		t.Fatalf("old completion replaced refreshed cache: %v %v", objects, err)
	}
}
