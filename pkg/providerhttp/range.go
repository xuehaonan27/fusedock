// Package providerhttp implements the HTTP proof used by conditional readers.
package providerhttp

import (
	"fmt"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var contentRange = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)

func StrongETag(value string) bool {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for i := 1; i < len(value)-1; i++ {
		if value[i] < 0x21 || value[i] == '"' || value[i] == 0x7f {
			return false
		}
	}
	return true
}

// Prepare binds every range request to the metadata-selected validator.
func Prepare(req *http.Request, offset, length int64, etag string) error {
	if offset < 0 || length <= 0 || offset > int64(^uint64(0)>>1)-length {
		return provider.ErrConflict
	}
	if etag != "" {
		if !StrongETag(etag) {
			return fmt.Errorf("%w: conditional reads require a strong ETag", provider.ErrUnsupported)
		}
		req.Header.Set("If-Match", etag)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	req.Header.Set("Accept-Encoding", "identity")
	return nil
}

// ReadResponse validates the original snapshot, including complete body length.
// etag is empty only for reads already bound by a provider-native revision.
func ReadResponse(resp *http.Response, dest []byte, offset, size int64, etag string) (int, error) {
	conflict := func(reason string) (int, error) { return 0, fmt.Errorf("%w: %s", provider.ErrConflict, reason) }
	length := int64(len(dest))
	if offset < 0 || length <= 0 || offset > size || length > size-offset {
		return conflict("range exceeds snapshot")
	}
	if resp.StatusCode == http.StatusPreconditionFailed || resp.StatusCode == http.StatusNotFound {
		return conflict("selected revision is unavailable")
	}
	if etag != "" && (!StrongETag(etag) || resp.Header.Get("ETag") != etag) {
		return conflict("response validator differs from snapshot")
	}
	if resp.Header.Get("Content-Encoding") != "" && !strings.EqualFold(resp.Header.Get("Content-Encoding"), "identity") {
		return conflict("encoded range is not snapshot bytes")
	}
	if resp.StatusCode == http.StatusPartialContent {
		fields := contentRange.FindStringSubmatch(resp.Header.Get("Content-Range"))
		if fields == nil {
			return conflict("invalid Content-Range")
		}
		first, e1 := strconv.ParseInt(fields[1], 10, 64)
		last, e2 := strconv.ParseInt(fields[2], 10, 64)
		total, e3 := strconv.ParseInt(fields[3], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || first != offset || last != offset+length-1 || total != size {
			return conflict("Content-Range differs from snapshot")
		}
	} else if etag != "" || resp.StatusCode != http.StatusOK || offset != 0 || length != size {
		return conflict("server did not honor the range")
	}
	if resp.ContentLength >= 0 && resp.ContentLength != length {
		return conflict("response length differs from snapshot")
	}
	n, err := io.ReadFull(resp.Body, dest)
	if err != nil {
		return n, err
	}
	var extra [1]byte
	if count, err := io.ReadFull(resp.Body, extra[:]); count != 0 || err != io.EOF {
		return conflict("response has extra bytes")
	}
	return n, nil
}
