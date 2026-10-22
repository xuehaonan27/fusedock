package vfs

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

const defaultReadBlock = int64(4 << 20)

// reader coalesces the small reads issued by a file-system driver into larger
// provider ranges. It retains one block per open handle; a global disk cache is
// a separate layer and is not claimed by this implementation.
type reader struct {
	provider.Reader
	size  int64
	block int64

	mu     sync.Mutex
	start  int64
	data   []byte
	loaded bool
	closed bool
}

func (s *Session) OpenRead(ctx context.Context, object provider.Object) (provider.Reader, error) {
	if object.Size < 0 {
		return nil, errors.New("cannot read an object with unknown size")
	}
	handle, err := s.Session.OpenRead(ctx, object)
	if err != nil {
		return nil, err
	}
	return &reader{Reader: handle, size: object.Size, block: defaultReadBlock}, nil
}

func (r *reader) ReadAt(ctx context.Context, dest []byte, off int64) (int, error) {
	if off < 0 || off > r.size {
		return 0, errors.New("read offset is outside the file")
	}
	if len(dest) == 0 || off == r.size {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, errors.New("read handle is closed")
	}
	written := 0
	for written < len(dest) && off < r.size {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		if !r.covers(off) {
			if err := r.load(ctx, off); err != nil {
				if written > 0 && errors.Is(err, io.EOF) {
					return written, nil
				}
				return written, err
			}
		}
		cacheOffset := off - r.start
		available := min(int64(len(dest)-written), int64(len(r.data))-cacheOffset)
		if available <= 0 {
			return written, io.ErrUnexpectedEOF
		}
		copy(dest[written:], r.data[cacheOffset:cacheOffset+available])
		written += int(available)
		off += available
	}
	return written, nil
}

func (r *reader) covers(off int64) bool {
	return r.loaded && off >= r.start && off < r.start+int64(len(r.data))
}

func (r *reader) load(ctx context.Context, off int64) error {
	start := off / r.block * r.block
	length := min(r.block, r.size-start)
	if length <= 0 {
		return io.EOF
	}
	buf := make([]byte, int(length))
	n, err := r.Reader.ReadAt(ctx, buf, start)
	if n < 0 || n > len(buf) {
		return errors.New("provider returned an invalid read count")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n == 0 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	if n < len(buf) {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	r.start, r.data, r.loaded = start, buf, true
	return nil
}

func (r *reader) Close() error {
	r.mu.Lock()
	r.closed = true
	r.data = nil
	handle := r.Reader
	r.Reader = nil
	r.mu.Unlock()
	if handle == nil {
		return nil
	}
	return handle.Close()
}
