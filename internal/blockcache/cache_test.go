package blockcache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xuehaonan27/fusedock/internal/namespace"
)

func testKey(id string) Key {
	return Key{Node: namespace.NodeKey{Namespace: namespace.Namespace{AccountID: "acct_00000000000000000000000000000001", ProviderID: "test"}, ObjectID: id}, Version: "v1", Size: 64, Length: 64}
}
func newTestCache(t *testing.T, o Options) *Cache {
	t.Helper()
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if c.disk != nil {
		<-c.disk.ready
	}
	return c
}
func put(t *testing.T, c *Cache, k Key, access uint64) {
	t.Helper()
	l, err := c.Acquire(context.Background(), k, true, access, func(_ context.Context, b []byte) error {
		for i := range b {
			b[i] = byte(i)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
}
func TestCoalescedCancellationAndBudget(t *testing.T) {
	c := newTestCache(t, Options{MemoryBytes: 128, FillConcurrency: 2})
	start := make(chan struct{})
	finish := make(chan struct{})
	var calls atomic.Int32
	fill := func(ctx context.Context, b []byte) error {
		calls.Add(1)
		close(start)
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := c.Acquire(ctx, testKey("a"), false, 1, fill)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancel: %v", err)
		}
	}()
	<-start
	go func() {
		defer wg.Done()
		l, err := c.Acquire(context.Background(), testKey("a"), false, 2, fill)
		if err != nil {
			t.Error(err)
			return
		}
		l.Release()
	}()
	deadline := time.Now().Add(time.Second)
	for c.Stats().Coalesced != 1 {
		if time.Now().After(deadline) {
			t.Fatal("waiter did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	close(finish)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	for i := 0; i < 100; i++ {
		put(t, c, testKey(string(rune(i+100))), uint64(i))
		s := c.Stats()
		if s.MemoryBytes+s.ReservedBytes > s.MemoryLimit {
			t.Fatalf("budget: %+v", s)
		}
	}
}
func TestHotWorkingSetSurvivesScan(t *testing.T) {
	c := newTestCache(t, Options{MemoryBytes: 256})
	put(t, c, testKey("hot"), 1)
	put(t, c, testKey("hot"), 2)
	for i := 0; i < 100; i++ {
		put(t, c, testKey(string(rune(i+100))), uint64(i+10))
	}
	l, err := c.Acquire(context.Background(), testKey("hot"), false, 200, func(context.Context, []byte) error { return errors.New("hot entry evicted") })
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
}
func TestPersistentRestartIsolationAndCorruption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	o := Options{MemoryBytes: 128, DiskDirectory: dir, DiskBytes: 4096}
	c := newTestCache(t, o)
	k := testKey("a")
	put(t, c, k, 1)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c = newTestCache(t, o)
	noFetch := func(context.Context, []byte) error { return errors.New("unexpected network") }
	l, err := c.Acquire(context.Background(), k, true, 2, noFetch)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	other := k
	other.Node.Namespace.AccountID = "acct_00000000000000000000000000000002"
	if _, err := c.Acquire(context.Background(), other, true, 1, noFetch); err == nil {
		t.Fatal("cross-account hit")
	}
	c.Close()
	if err := os.WriteFile(filepath.Join(dir, blockPath(k)), []byte("torn"), 0600); err != nil {
		t.Fatal(err)
	}
	c = newTestCache(t, o)
	put(t, c, k, 3)
	if c.Stats().Corrupt != 1 {
		t.Fatal(c.Stats())
	}
	if n, err := c.Verify(); err != nil || n != 1 {
		t.Fatalf("verify: %d %v", n, err)
	}
}
func TestClearWhilePinned(t *testing.T) {
	c := newTestCache(t, Options{MemoryBytes: 128})
	k := testKey("a")
	fill := func(context.Context, []byte) error { return nil }
	old, err := c.Acquire(context.Background(), k, false, 1, fill)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Clear(k.Node.Namespace); err != nil {
		t.Fatal(err)
	}
	next, err := c.Acquire(context.Background(), k, false, 2, fill)
	if err != nil {
		t.Fatal(err)
	}
	old.Release()
	next.Release()
	if c.Stats().MemoryBytes != 64 {
		t.Fatal(c.Stats())
	}
}
func TestCloseCancelsBlockedReservation(t *testing.T) {
	c := newTestCache(t, Options{MemoryBytes: 64})
	l, err := c.Acquire(context.Background(), testKey("a"), false, 1, func(context.Context, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Acquire(context.Background(), testKey("b"), false, 1, func(context.Context, []byte) error { return nil })
		done <- err
	}()
	for c.Stats().Flights == 0 {
		time.Sleep(time.Millisecond)
	}
	c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed fill succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("close hung")
	}
	l.Release()
}
