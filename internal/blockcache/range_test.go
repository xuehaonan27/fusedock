package blockcache

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDemandRangesAndCompleteBlockPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	options := Options{MemoryBytes: 128, DiskDirectory: dir, DiskBytes: 4096}
	c := newTestCache(t, options)
	k := testKey("partial")
	var fetched atomic.Int64
	fill := func(_ context.Context, b []byte, off int64, _ func(int)) error {
		fetched.Add(int64(len(b)))
		for i := range b {
			b[i] = byte(off + int64(i))
		}
		return nil
	}
	read := func(off, n int64) {
		t.Helper()
		l, err := c.AcquireRange(context.Background(), k, true, 1, off, n, fill)
		if err != nil {
			t.Fatal(err)
		}
		for i, v := range l.Bytes() {
			if v != byte(off+int64(i)) {
				t.Fatalf("byte %d = %d", i, v)
			}
		}
		l.Release()
	}
	read(10, 10)
	read(15, 10)
	if fetched.Load() != 15 || c.Stats().DiskBytes != 0 {
		t.Fatalf("partial: fetched=%d %+v", fetched.Load(), c.Stats())
	}
	read(0, 64)
	if fetched.Load() != 64 {
		t.Fatalf("overlapping ranges fetched %d bytes", fetched.Load())
	}
	c.Close()
	c = newTestCache(t, options)
	l, err := c.AcquireRange(context.Background(), k, true, 2, 20, 5, func(context.Context, []byte, int64, func(int)) error { return errors.New("unexpected download") })
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	if c.Stats().DiskHits != 1 {
		t.Fatal(c.Stats())
	}
}

func TestForegroundReturnsBeforePrefetchTail(t *testing.T) {
	c := newTestCache(t, Options{MemoryBytes: 128})
	k := testKey("stream")
	started, allowPrefix, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(finish)
	done := make(chan error, 1)
	go func() {
		l, err := c.AcquireRange(context.Background(), k, false, 1, 0, 64, func(ctx context.Context, b []byte, _ int64, publish func(int)) error {
			close(started)
			select {
			case <-allowPrefix:
			case <-ctx.Done():
				return ctx.Err()
			}
			copy(b, bytes.Repeat([]byte{7}, 16))
			publish(16)
			select {
			case <-finish:
			case <-ctx.Done():
				return ctx.Err()
			}
			for i := 16; i < len(b); i++ {
				b[i] = 8
			}
			return nil
		})
		if l != nil {
			l.Release()
		}
		done <- err
	}()
	<-started
	foreground := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		l, err := c.AcquireRange(ctx, k, false, 2, 0, 8, func(context.Context, []byte, int64, func(int)) error { return errors.New("duplicate fill") })
		if l != nil {
			if !bytes.Equal(l.Bytes(), bytes.Repeat([]byte{7}, 8)) {
				err = errors.New("wrong prefix")
			}
			l.Release()
		}
		foreground <- err
	}()
	for c.Stats().Coalesced == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("foreground did not join prefetch")
		case <-time.After(time.Millisecond):
		}
	}
	close(allowPrefix)
	if err := <-foreground; err != nil {
		t.Fatalf("foreground waited for response tail: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("tail finished early: %v", err)
	default:
	}
}

func TestPartialFailureDoesNotPersistAndCanResume(t *testing.T) {
	c := newTestCache(t, Options{MemoryBytes: 128, DiskDirectory: filepath.Join(t.TempDir(), "cache"), DiskBytes: 4096})
	k := testKey("short")
	_, err := c.AcquireRange(context.Background(), k, true, 1, 0, 64, func(_ context.Context, b []byte, _ int64, publish func(int)) error {
		for i := 0; i < 16; i++ {
			b[i] = 9
		}
		publish(16)
		return errors.New("connection lost")
	})
	if err == nil || c.Stats().DiskBytes != 0 {
		t.Fatalf("partial error: %v %+v", err, c.Stats())
	}
	var requested int64
	l, err := c.AcquireRange(context.Background(), k, true, 2, 0, 64, func(_ context.Context, b []byte, off int64, _ func(int)) error {
		requested = off
		for i := range b {
			b[i] = 9
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	if requested != 16 {
		t.Fatalf("resume starts at %d", requested)
	}
}

func TestAtomicFillDoesNotReuseIncompleteRangeProof(t *testing.T) {
	c := newTestCache(t, Options{MemoryBytes: 128})
	k := testKey("atomic")
	l, err := c.AcquireRange(context.Background(), k, false, 1, 10, 10, func(_ context.Context, b []byte, _ int64, _ func(int)) error { clear(b); return nil })
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	l, err = c.Acquire(context.Background(), k, false, 2, func(_ context.Context, b []byte) error {
		if len(b) != 64 {
			t.Errorf("atomic proof received %d bytes", len(b))
		}
		clear(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
}

func TestMemoryRangeHitDoesNotWaitForDisk(t *testing.T) {
	c := newTestCache(t, Options{MemoryBytes: 128, DiskDirectory: filepath.Join(t.TempDir(), "cache"), DiskBytes: 4096})
	k := testKey("disk-busy")
	put(t, c, k, 1)
	c.disk.mu.Lock()
	done := make(chan error, 1)
	go func() {
		l, err := c.AcquireRange(context.Background(), k, true, 2, 0, 8, func(context.Context, []byte, int64, func(int)) error { return errors.New("unexpected download") })
		if l != nil {
			l.Release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		c.disk.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		c.disk.mu.Unlock()
		t.Fatal("memory hit waited for disk IO")
	}
}

func TestOverlappingConcurrentRangesDownloadEachByteOnce(t *testing.T) {
	c := newTestCache(t, Options{MemoryBytes: 256})
	k := testKey("overlap")
	var counts [64]atomic.Int32
	fill := func(_ context.Context, b []byte, off int64, publish func(int)) error {
		for i := range b {
			counts[int(off)+i].Add(1)
			b[i] = byte(int(off) + i)
		}
		publish(len(b) / 2)
		return nil
	}
	done := make(chan error, 32)
	for i := 0; i < 32; i++ {
		go func(off int64) {
			l, err := c.AcquireRange(context.Background(), k, false, 1, off, 32, fill)
			if err == nil {
				for j, v := range l.Bytes() {
					if v != byte(int(off)+j) {
						err = errors.New("wrong byte")
					}
				}
				l.Release()
			}
			done <- err
		}(int64(i))
	}
	for i := 0; i < 32; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 63; i++ {
		if n := counts[i].Load(); n != 1 {
			t.Fatalf("byte %d downloaded %d times", i, n)
		}
	}
}

func TestProviderCancellationIsReturnedWithoutRetry(t *testing.T) {
	c := newTestCache(t, Options{MemoryBytes: 128})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	_, err := c.AcquireRange(ctx, testKey("cancelled-provider"), false, 1, 0, 8, func(context.Context, []byte, int64, func(int)) error { calls++; return context.Canceled })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
