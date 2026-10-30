package vfs

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type rangeReader struct {
	data  []byte
	calls atomic.Int32
}

func (r *rangeReader) ReadAt(_ context.Context, dest []byte, off int64) (int, error) {
	r.calls.Add(1)
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(dest, r.data[off:])
	if n < len(dest) {
		return n, io.EOF
	}
	return n, nil
}

func (r *rangeReader) Close() error { return nil }

type readSession struct {
	fakeSession
	reader *rangeReader
}

func (s *readSession) OpenRead(context.Context, provider.Object) (provider.Reader, error) {
	return s.reader, nil
}

func TestReadHandleCoalescesDriverReads(t *testing.T) {
	data := make([]byte, defaultReadBlock*2)
	for i := range data {
		data[i] = byte(i % 251)
	}
	handle := &rangeReader{data: data}
	session := New(&readSession{reader: handle}, 0)
	object := provider.Object{ID: "file", Kind: provider.KindFile, Size: int64(len(data))}
	reader, err := session.OpenRead(context.Background(), object)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	buf := make([]byte, 32<<10)
	for off := int64(0); off < 128<<10; off += int64(len(buf)) {
		if _, err := reader.ReadAt(context.Background(), buf, off); err != nil {
			t.Fatal(err)
		}
	}
	if calls := handle.calls.Load(); calls != 1 {
		t.Fatalf("sequential reads made %d provider calls", calls)
	}
	if _, err := reader.ReadAt(context.Background(), buf, defaultReadBlock-1); err != nil {
		t.Fatal(err)
	}
	if calls := handle.calls.Load(); calls != 2 {
		t.Fatalf("cross-block read made %d provider calls", calls)
	}
}

func TestReadHandleRejectsNegativeOffset(t *testing.T) {
	session := New(&readSession{reader: &rangeReader{data: []byte("x")}}, 0)
	reader, err := session.OpenRead(context.Background(), provider.Object{ID: "file", Kind: provider.KindFile, Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ReadAt(context.Background(), make([]byte, 1), -1); err == nil {
		t.Fatal("negative offset was accepted")
	}
}
