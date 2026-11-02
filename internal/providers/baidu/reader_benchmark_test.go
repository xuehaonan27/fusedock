package baidu

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xuehaonan27/fusedock/internal/vfs"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

// A controlled HTTP server emits 32 KiB per millisecond. The baseline performs
// the former 4 MiB fill; this is not a measurement of Baidu's network service.
func BenchmarkBaiduReadDelivery(b *testing.B) {
	for _, pattern := range []string{"first_32KiB", "sequential_4MiB"} {
		for _, mode := range []string{"block_baseline", "demand_prefetch"} {
			b.Run(pattern+"/"+mode, func(b *testing.B) {
				const size = int64(4 << 20)
				var requested, requests atomic.Int64
				c, links := rangeClient(b, func(w http.ResponseWriter, r *http.Request) {
					_, n := replyRange(w, r, size)
					requested.Add(n)
					requests.Add(1)
					part := make([]byte, 32<<10)
					for n > 0 {
						select {
						case <-time.After(time.Millisecond):
						case <-r.Context().Done():
							return
						}
						count := min(n, int64(len(part)))
						if _, err := w.Write(part[:count]); err != nil {
							return
						}
						w.(http.Flusher).Flush()
						n -= count
					}
				})
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if mode == "block_baseline" {
						r := &reader{client: c, entry: meta{ID: "42", Size: size}}
						if _, err := r.ReadAt(context.Background(), make([]byte, size), 0); err != nil {
							b.Fatal(err)
						}
					} else {
						raw := &Session{client: c, known: map[string]meta{"42": {ID: "42", Size: size}}}
						s := vfs.New(raw, 0)
						r, err := s.OpenRead(context.Background(), provider.Object{ID: "42", Kind: provider.KindFile, Size: size})
						if err != nil {
							b.Fatal(err)
						}
						limit := int64(32 << 10)
						if pattern == "sequential_4MiB" {
							limit = size
						}
						buf := make([]byte, 32<<10)
						for off := int64(0); off < limit; off += int64(len(buf)) {
							if _, err := r.ReadAt(context.Background(), buf, off); err != nil {
								b.Fatal(err)
							}
						}
						r.Close()
						s.Close()
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(requested.Load())/float64(b.N), "remote-B/op")
				b.ReportMetric(float64(requests.Load())/float64(b.N), "GET/op")
				b.ReportMetric(float64(links.Load())/float64(b.N), "dlink/op")
			})
		}
	}
}
