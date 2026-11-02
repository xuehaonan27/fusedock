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

func TestSmallReadDoesNotDownloadWholeBlock(t *testing.T) {
	data := make([]byte, defaultReadBlock*2)
	for i := range data {
		data[i] = byte(i % 251)
	}
	handle := &rangeReader{data: data}
	session := New(&readSession{reader: handle}, 0)
	defer session.Close()
	reader, err := session.OpenRead(context.Background(), provider.Object{ID: "file", Kind: provider.KindFile, Size: int64(len(data))})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	buf := make([]byte, 32<<10)
	for i := 0; i < 2; i++ {
		if _, err := reader.ReadAt(context.Background(), buf, 0); err != nil {
			t.Fatal(err)
		}
	}
	if calls := handle.calls.Load(); calls != 1 {
		t.Fatalf("repeated range fetched %d times", calls)
	}
	if bytes := session.CacheStats().NetworkBytes; bytes != uint64(len(buf)) {
		t.Fatalf("small read fetched %d bytes", bytes)
	}
	if _, err := reader.ReadAt(context.Background(), buf, defaultReadBlock-1); err != nil {
		t.Fatal(err)
	}
	for i, v := range buf {
		if v != data[int(defaultReadBlock)-1+i] {
			t.Fatalf("cross-block byte %d", i)
		}
	}
	if bytes := session.CacheStats().NetworkBytes; bytes != uint64(2*len(buf)) {
		t.Fatalf("head/tail reads fetched %d bytes", bytes)
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
