package providertest

import (
	"context"
	"io"
	"testing"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type memorySession struct{ data []byte }

func (*memorySession) Root(context.Context) (provider.Object, error) {
	return provider.Object{ID: "root", Name: "root", Kind: provider.KindDirectory}, nil
}
func (*memorySession) Stat(_ context.Context, object provider.Object) (provider.Object, error) {
	return object, nil
}
func (*memorySession) List(context.Context, provider.Object) ([]provider.Object, error) {
	return []provider.Object{{ID: "file", ParentID: "root", Name: "file", Kind: provider.KindFile, Size: 4}}, nil
}
func (s *memorySession) OpenRead(context.Context, provider.Object) (provider.Reader, error) {
	return &memoryReader{data: s.data}, nil
}
func (*memorySession) Close() error { return nil }

type memoryReader struct{ data []byte }

func (r *memoryReader) ReadAt(_ context.Context, dest []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(dest, r.data[off:])
	if n < len(dest) {
		return n, io.EOF
	}
	return n, nil
}
func (*memoryReader) Close() error { return nil }

func TestConformanceFixture(t *testing.T) {
	session := &memorySession{data: []byte("test")}
	object := provider.Object{ID: "file", Name: "file", Kind: provider.KindFile, Size: 4}
	Run(t, session, Options{ReadObject: &object, ReadLength: 2})
}
