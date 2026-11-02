package fskit_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	native "github.com/xuehaonan27/fusedock/internal/driver/fskit"
	"github.com/xuehaonan27/fusedock/internal/providers/local"
	"github.com/xuehaonan27/fusedock/internal/vfs"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type measuredSession struct {
	provider.Session
	mu     sync.Mutex
	ranges [][2]int64
}

func (s *measuredSession) OpenRead(ctx context.Context, o provider.Object) (provider.Reader, error) {
	r, err := s.Session.OpenRead(ctx, o)
	if err != nil {
		return nil, err
	}
	return &measuredReader{Reader: r, session: s}, nil
}

type measuredReader struct {
	provider.Reader
	session *measuredSession
}

func (r *measuredReader) ReadAt(ctx context.Context, b []byte, off int64) (int, error) {
	r.session.mu.Lock()
	r.session.ranges = append(r.session.ranges, [2]int64{off, int64(len(b))})
	r.session.mu.Unlock()
	return r.Reader.ReadAt(ctx, b, off)
}

func TestFSKitSmallReadFetchGranularity(t *testing.T) {
	if os.Getenv("FUSEDOCK_FSKIT_TEST") != "1" {
		t.Skip("requires installed and enabled FSKit extension")
	}
	root, point := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "image.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(8 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	raw, err := local.New().Open(context.Background(), provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	measured := &measuredSession{Session: raw}
	session := vfs.New(measured, time.Second)
	defer session.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- native.Mount(ctx, session, point, native.Options{ReadOnly: true, Ready: func() { close(ready) }})
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Error("native unmount timed out")
		}
	}()
	select {
	case <-ready:
	case err := <-done:
		done <- nil
		t.Fatalf("mount: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("mount not ready")
	}
	f, err := os.Open(filepath.Join(point, "image.bin"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	n, err := f.ReadAt(make([]byte, 32<<10), 0)
	elapsed := time.Since(start)
	f.Close()
	if err != nil || n != 32<<10 {
		t.Fatalf("read=%d %v", n, err)
	}
	measured.mu.Lock()
	ranges := append([][2]int64(nil), measured.ranges...)
	measured.mu.Unlock()
	t.Logf("32 KiB application read: provider ranges=%v elapsed=%s", ranges, elapsed)
	if len(ranges) == 0 {
		t.Fatal("no provider read")
	}
	for _, r := range ranges {
		if r[1] >= 4<<20 {
			t.Fatalf("small native read still fetches a whole 4 MiB block: %v", ranges)
		}
	}
}
