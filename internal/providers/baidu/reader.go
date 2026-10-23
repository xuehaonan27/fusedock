package baidu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

type reader struct {
	client *client
	entry  meta
}

func (r *reader) ReadAt(ctx context.Context, dest []byte, offset int64) (int, error) {
	if offset < 0 || offset > r.entry.Size {
		return 0, errors.New("read offset is outside the Baidu file")
	}
	if len(dest) == 0 || offset == r.entry.Size {
		return 0, nil
	}
	length := min(int64(len(dest)), r.entry.Size-offset)
	link, err := r.client.downloadURL(ctx, r.entry.ID)
	if err != nil {
		return 0, err
	}
	u, err := url.Parse(link)
	if err != nil || !trustedDownloadURL(u, r.client.apiBase) {
		return 0, errors.New("Baidu returned an invalid download URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, errors.New("invalid Baidu download request")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	resp, err := r.client.download.Do(req)
	if err != nil {
		return 0, safeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPartialContent {
		var first, last, total int64
		if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &first, &last, &total); err != nil || first != offset || last != offset+length-1 || total != r.entry.Size {
			return 0, errors.New("Baidu returned an inconsistent byte range")
		}
	} else if resp.StatusCode != http.StatusOK || offset != 0 {
		if resp.StatusCode == http.StatusOK {
			return 0, fmt.Errorf("%w: Baidu ignored the requested byte range", provider.ErrUnsupported)
		}
		return 0, fmt.Errorf("Baidu download HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength < length {
		return 0, errors.New("Baidu download returned an incomplete range")
	}
	n, err := io.ReadFull(resp.Body, dest[:length])
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return n, io.ErrUnexpectedEOF
		}
		return n, err
	}
	return n, nil
}

func (r *reader) Close() error { return nil }
