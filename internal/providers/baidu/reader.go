package baidu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type reader struct {
	client   *client
	entry    meta
	linkOnce sync.Once
	linkLock chan struct{}
	link     string
}

func (r *reader) ReadAt(ctx context.Context, dest []byte, offset int64) (int, error) {
	return r.ReadAtProgress(ctx, dest, offset, nil)
}

// ReadAtProgress validates range headers before exposing prefixes. Each open
// reader reuses its signed link; an expiry response renews it once per request.
func (r *reader) ReadAtProgress(ctx context.Context, dest []byte, offset int64, publish func(int)) (int, error) {
	if offset < 0 || offset > r.entry.Size {
		return 0, errors.New("read offset is outside the Baidu file")
	}
	if len(dest) == 0 || offset == r.entry.Size {
		return 0, nil
	}
	length := min(int64(len(dest)), r.entry.Size-offset)
	link, reused, err := r.downloadLink(ctx, "")
	if err != nil {
		return 0, err
	}
	resp, err := r.requestRange(ctx, link, offset, length)
	if err != nil {
		return 0, err
	}
	if reused && expiredLinkStatus(resp.StatusCode) {
		resp.Body.Close()
		link, _, err = r.downloadLink(ctx, link)
		if err != nil {
			return 0, err
		}
		resp, err = r.requestRange(ctx, link, offset, length)
		if err != nil {
			return 0, err
		}
	}
	defer resp.Body.Close()
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return 0, fmt.Errorf("%w: encoded Baidu range is not file bytes", provider.ErrConflict)
	}
	if resp.StatusCode == http.StatusPartialContent {
		var first, last, total int64
		if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &first, &last, &total); err != nil || first != offset || last != offset+length-1 || total != r.entry.Size {
			return 0, fmt.Errorf("%w: inconsistent Baidu byte range", provider.ErrConflict)
		}
	} else if resp.StatusCode != http.StatusOK || offset != 0 || length != r.entry.Size {
		if resp.StatusCode == http.StatusOK {
			return 0, fmt.Errorf("%w: Baidu ignored the requested byte range", provider.ErrUnsupported)
		}
		return 0, fmt.Errorf("Baidu download HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != length {
		return 0, errors.New("Baidu download returned an incomplete range")
	}
	n := 0
	for int64(n) < length {
		end := min(length, int64(n)+(32<<10))
		count, err := io.ReadFull(resp.Body, dest[n:end])
		n += count
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return n, io.ErrUnexpectedEOF
			}
			return n, err
		}
		if publish != nil && int64(n) < length {
			publish(n)
		}
	}
	var extra [1]byte
	if count, err := io.ReadFull(resp.Body, extra[:]); count != 0 || err != io.EOF {
		return 0, fmt.Errorf("%w: Baidu response exceeds selected size", provider.ErrConflict)
	}
	if publish != nil {
		publish(n)
	}
	return n, nil
}

func (r *reader) requestRange(ctx context.Context, link string, offset, length int64) (*http.Response, error) {
	u, err := url.Parse(link)
	if err != nil || !trustedDownloadURL(u, r.client.apiBase) {
		return nil, errors.New("Baidu returned an invalid download URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid Baidu download request")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	resp, err := r.client.download.Do(req)
	if err != nil {
		return nil, safeTransportError(err)
	}
	return resp, nil
}

func expiredLinkStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusGone
}

func (r *reader) downloadLink(ctx context.Context, expired string) (string, bool, error) {
	r.linkOnce.Do(func() { r.linkLock = make(chan struct{}, 1) })
	select {
	case r.linkLock <- struct{}{}:
		defer func() { <-r.linkLock }()
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
	if r.link != "" && r.link != expired {
		return r.link, true, nil
	}
	r.link = ""
	link, err := r.client.downloadURL(ctx, r.entry.ID)
	if err == nil {
		r.link = link
	}
	return link, false, err
}

func (r *reader) Close() error { return nil }
