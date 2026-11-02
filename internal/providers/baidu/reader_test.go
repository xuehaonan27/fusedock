package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xuehaonan27/fusedock/internal/vfs"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func rangeClient(t testing.TB, handler http.HandlerFunc) (*client, *atomic.Int32) {
	t.Helper()
	links := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/2.0/xpan/multimedia" {
			generation := links.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []map[string]any{{"dlink": fmt.Sprintf("%s/download?generation=%d", serverURL(r), generation)}}})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	c, err := newClient(server.URL, server.URL, "app", "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.restore(savedState{AccessToken: "test-token", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	t.Cleanup(c.close)
	return c, links
}

func replyRange(w http.ResponseWriter, r *http.Request, size int64) (int64, int64) {
	var off, last int64
	fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &off, &last)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, last, size))
	w.Header().Set("Content-Length", fmt.Sprint(last-off+1))
	w.WriteHeader(http.StatusPartialContent)
	return off, last - off + 1
}

func TestConcurrentRangesReuseAndRenewDownloadLink(t *testing.T) {
	var expired atomic.Bool
	c, links := rangeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if expired.Load() && r.URL.Query().Get("generation") == "1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, n := replyRange(w, r, 1024)
		w.Write(make([]byte, n))
	})
	r := &reader{client: c, entry: meta{ID: "42", Size: 1024}}
	batch := func() {
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(off int64) {
				defer wg.Done()
				if _, err := r.ReadAt(context.Background(), make([]byte, 8), off); err != nil {
					t.Error(err)
				}
			}(int64(i * 8))
		}
		wg.Wait()
	}
	batch()
	if links.Load() != 1 {
		t.Fatalf("first ranges fetched %d links", links.Load())
	}
	expired.Store(true)
	batch()
	if links.Load() != 2 {
		t.Fatalf("renewal fetched %d links", links.Load())
	}
}

func TestVFSBaiduDemandReadAndStreamingPrefetch(t *testing.T) {
	const size = int64(8 << 20)
	const chunk = int64(32 << 10)
	prefetchStarted := make(chan struct{})
	prefetchCancelled := make(chan struct{})
	var requests atomic.Int32
	c, links := rangeClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		off, n := replyRange(w, r, size)
		if off == 2*chunk && n == 2*chunk {
			w.Write(make([]byte, chunk))
			w.(http.Flusher).Flush()
			close(prefetchStarted)
			<-r.Context().Done()
			close(prefetchCancelled)
			return
		}
		if n != chunk {
			t.Errorf("foreground requested %d bytes at %d", n, off)
		}
		w.Write(make([]byte, n))
	})
	raw := &Session{client: c, known: map[string]meta{"42": {ID: "42", Size: size}}}
	s := vfs.New(raw, 0)
	defer s.Close()
	r, err := s.OpenRead(context.Background(), provider.Object{ID: "42", Kind: provider.KindFile, Size: size})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	read := func(off int64) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if n, err := r.ReadAt(ctx, make([]byte, chunk), off); err != nil || n != int(chunk) {
			t.Fatalf("read at %d: %d %v", off, n, err)
		}
	}
	read(0)
	if bytes := s.CacheStats().NetworkBytes; bytes != uint64(chunk) {
		t.Fatalf("first read fetched %d bytes", bytes)
	}
	read(chunk)
	select {
	case <-prefetchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("sequential read did not prefetch")
	}
	read(2 * chunk)    // Must return while the HTTP response's remaining half is blocked.
	read(size - chunk) // A seek must cancel the unused tail.
	select {
	case <-prefetchCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("seek did not cancel prefetch")
	}
	if requests.Load() != 4 || links.Load() != 1 {
		t.Fatalf("downloads=%d links=%d", requests.Load(), links.Load())
	}
}

func TestProgressiveReadRejectsHeadersBeforePublishing(t *testing.T) {
	for _, invalid := range []string{"range", "encoding"} {
		t.Run(invalid, func(t *testing.T) {
			c, _ := rangeClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("missing identity encoding request")
				}
				contentRange := "bytes 0-65535/100000"
				if invalid == "range" {
					contentRange = "bytes 1-65536/100000"
				} else {
					w.Header().Set("Content-Encoding", "gzip")
				}
				w.Header().Set("Content-Range", contentRange)
				w.WriteHeader(http.StatusPartialContent)
				w.Write(make([]byte, 65536))
			})
			r := &reader{client: c, entry: meta{ID: "42", Size: 100000}}
			published := 0
			_, err := r.ReadAtProgress(context.Background(), make([]byte, 65536), 0, func(n int) { published = n })
			if !errors.Is(err, provider.ErrConflict) || published != 0 {
				t.Fatalf("bad headers: published=%d error=%v", published, err)
			}
		})
	}
}

func TestTinySequentialHeadersDoNotGrowLargePrefetch(t *testing.T) {
	const size = int64(8 << 20)
	var requested atomic.Int64
	c, _ := rangeClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, n := replyRange(w, r, size)
		requested.Add(n)
		w.Write(make([]byte, n))
	})
	raw := &Session{client: c, known: map[string]meta{"42": {ID: "42", Size: size}}}
	s := vfs.New(raw, 0)
	defer s.Close()
	r, err := s.OpenRead(context.Background(), provider.Object{ID: "42", Kind: provider.KindFile, Size: size})
	if err != nil {
		t.Fatal(err)
	}
	for off := int64(0); off < 1024; off += 8 {
		if _, err := r.ReadAt(context.Background(), make([]byte, 8), off); err != nil {
			r.Close()
			t.Fatal(err)
		}
	}
	r.Close()
	if n := requested.Load(); n > 128<<10 {
		t.Fatalf("1 KiB of tiny header reads fetched %d bytes", n)
	}
}
