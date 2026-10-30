package providerhttp

import (
	"errors"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestConditionalRanges(t *testing.T) {
	for _, tc := range []struct {
		name, etag, header, body string
		status                   int
		valid                    bool
	}{{"match", `"v1"`, "bytes 1-3/5", "bcd", 206, true}, {"weak", `W/"v1"`, "bytes 1-3/5", "bcd", 206, false}, {"changed", `"v2"`, "bytes 1-3/5", "bcd", 206, false}, {"whole", `"v1"`, "", "abcde", 200, false}, {"total", `"v1"`, "bytes 1-3/6", "bcd", 206, false}, {"extra", `"v1"`, "bytes 1-3/5", "bcde", 206, false}, {"precondition", `"v1"`, "", "", 412, false}, {"garbage", `"v1"`, "bytes 1-3/5junk", "bcd", 206, false}} {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{StatusCode: tc.status, Header: http.Header{"Etag": {tc.etag}, "Content-Range": {tc.header}}, Body: io.NopCloser(strings.NewReader(tc.body)), ContentLength: -1}
			_, err := ReadResponse(response, make([]byte, 3), 1, 5, `"v1"`)
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && !errors.Is(err, provider.ErrConflict) {
				t.Fatalf("conflict: %v", err)
			}
		})
	}
}
