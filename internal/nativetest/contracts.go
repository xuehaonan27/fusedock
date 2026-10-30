// Package nativetest exercises observable filesystem behavior on the host OS.
package nativetest

import (
	"bytes"
	"context"
	"errors"
	"github.com/xuehaonan27/fusedock/internal/vfs"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type Mount func(context.Context, provider.Session, string, func()) error
type remote struct {
	mu       sync.Mutex
	version  string
	name     string
	objectID string
	content  map[string][]byte
}

func (s *remote) Root(context.Context) (provider.Object, error) {
	return provider.Object{ID: "root", Name: "fixture", Kind: provider.KindDirectory}, nil
}
func (s *remote) object() provider.Object {
	return provider.Object{ID: s.objectID, ParentID: "root", Name: s.name, Kind: provider.KindFile, Size: int64(len(s.content[s.version])), ContentVersion: s.version, Version: s.version}
}
func (s *remote) Stat(_ context.Context, o provider.Object) (provider.Object, error) {
	if o.ID == "root" {
		return s.Root(context.Background())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.object(), nil
}
func (s *remote) List(context.Context, provider.Object) ([]provider.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []provider.Object{s.object()}, nil
}
func (s *remote) OpenRead(ctx context.Context, o provider.Object) (provider.Reader, error) {
	return s.OpenReadRevision(ctx, o.Revision())
}
func (s *remote) OpenReadRevision(_ context.Context, r provider.ObjectRevision) (provider.Reader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.content[r.ContentVersion]
	if !ok {
		return nil, provider.ErrConflict
	}
	return &read{b: b}, nil
}
func (s *remote) ReadCapabilities(context.Context) (provider.ReadCapabilities, error) {
	return provider.ReadCapabilities{StableObjectID: true, ExactRange: true}, nil
}
func (s *remote) Close() error { return nil }

type read struct{ b []byte }

func (r *read) ReadAt(ctx context.Context, b []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if off >= int64(len(r.b)) {
		return 0, io.EOF
	}
	return copy(b, r.b[off:]), nil
}
func (r *read) Close() error { return nil }
func Run(t *testing.T, mount Mount, prepare ...func(string) error) {
	t.Helper()
	original := append([]byte("original snapshot"), bytes.Repeat([]byte("A"), 8<<20)...)
	modified := append([]byte("modified snapshot with a new size"), bytes.Repeat([]byte("B"), 8<<20)...)
	replaced := bytes.Repeat([]byte("C"), len(modified))
	base := &remote{version: "v1", name: "file.txt", objectID: "file", content: map[string][]byte{"v1": original, "v2": modified, "v3": replaced}}
	session := vfs.New(base, 0)
	defer session.Close()
	point := t.TempDir()
	for _, setup := range prepare {
		if err := setup(point); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- mount(ctx, session, point, func() { close(ready) }) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Error("native unmount timed out")
		}
	}()
	select {
	case <-ready:
	case err := <-done:
		done <- nil
		t.Fatalf("native mount failed: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("native mount did not become ready")
	}
	path := filepath.Join(point, "file.txt")
	entries, err := os.ReadDir(point)
	if err != nil || len(entries) != 1 || entries[0].Name() != "file.txt" {
		t.Fatalf("native directory listing: %v %v", entries, err)
	}
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	prefix := make([]byte, 4)
	if _, err := old.ReadAt(prefix, 0); err != nil || string(prefix) != "orig" {
		t.Fatalf("initial read: %q %v", prefix, err)
	}
	before, err := old.Stat()
	if err != nil {
		t.Fatal(err)
	}
	base.mu.Lock()
	base.version = "v2"
	base.mu.Unlock()
	session.Invalidate("")
	deadline := time.Now().Add(5 * time.Second)
	var current []byte
	for time.Now().Before(deadline) {
		current, err = os.ReadFile(path)
		if err == nil && bytes.Equal(current, modified) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !bytes.Equal(current, modified) {
		t.Fatalf("reopen returned an incorrect revision (%d bytes): %v", len(current), err)
	}
	if _, err := old.ReadAt(prefix, 0); err != nil || string(prefix) != "orig" {
		t.Fatalf("old cached data changed: %q %v", prefix, err)
	}
	if _, err := old.ReadAt(prefix, 8<<20); err != nil || string(prefix) != "AAAA" {
		t.Fatalf("old uncached range changed: %q %v", prefix, err)
	}
	cross := make([]byte, 8)
	if _, err := old.ReadAt(cross, (4<<20)-3); err != nil || string(cross) != "AAAAAAAA" {
		t.Fatalf("cross-block snapshot read: %q %v", cross, err)
	}
	after, err := old.Stat()
	if err != nil || after.Size() != before.Size() {
		t.Fatal("open snapshot size changed")
	}
	var eof [1]byte
	if n, err := old.ReadAt(eof[:], before.Size()); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("EOF=(%d,%v)", n, err)
	}
	old.Close()
	base.mu.Lock()
	base.version = "v3"
	base.mu.Unlock()
	session.Invalidate("")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err = os.ReadFile(path)
		if err == nil && bytes.Equal(current, replaced) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !bytes.Equal(current, replaced) {
		t.Fatal("same-size content change was not observed")
	}
	base.mu.Lock()
	base.name = "renamed.txt"
	base.mu.Unlock()
	session.Invalidate("")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(point, "renamed.txt")); err == nil {
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("remote rename was not observed")
}
