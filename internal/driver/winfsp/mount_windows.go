package winfsp

import (
	"context"
	"errors"
	"github.com/winfsp/cgofuse/fuse"
	"github.com/xuehaonan27/fusedock/internal/namespace"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"io"
	"strings"
	"sync"
)

type handle struct {
	mu     sync.RWMutex
	object provider.Object
	reader provider.Reader
	closed bool
}
type filesystem struct {
	fuse.FileSystemBase
	session provider.Session
	ctx     context.Context
	ns      namespace.Namespace
	ids     namespace.Nodes
	mu      sync.Mutex
	next    uint64
	handles map[uint64]*handle
	ready   chan struct{}
}

func (f *filesystem) Init() { close(f.ready) }
func (f *filesystem) resolve(path string) (provider.Object, error) {
	return provider.Resolve(f.ctx, f.session, strings.TrimPrefix(path, "/"))
}
func (f *filesystem) Open(path string, flags int) (int, uint64) {
	if flags&fuse.O_ACCMODE != fuse.O_RDONLY {
		return -fuse.EROFS, ^uint64(0)
	}
	o, err := f.resolve(path)
	if err != nil {
		return errno(err), ^uint64(0)
	}
	if o.IsDir() {
		return -fuse.EISDIR, ^uint64(0)
	}
	r, err := f.session.OpenRead(f.ctx, o)
	if err != nil {
		return errno(err), ^uint64(0)
	}
	if s, ok := r.(provider.SnapshotReader); ok {
		o = s.Snapshot()
	}
	f.mu.Lock()
	f.next++
	id := f.next
	f.handles[id] = &handle{object: o, reader: r}
	f.mu.Unlock()
	return 0, id
}
func (f *filesystem) get(id uint64) *handle { f.mu.Lock(); defer f.mu.Unlock(); return f.handles[id] }
func (f *filesystem) Getattr(path string, out *fuse.Stat_t, id uint64) int {
	var o provider.Object
	var err error
	if h := f.get(id); h != nil {
		o = h.object
	} else {
		o, err = f.resolve(path)
	}
	if err != nil {
		return errno(err)
	}
	*out = f.attrs(o)
	return 0
}
func (f *filesystem) attrs(o provider.Object) fuse.Stat_t {
	mode := uint32(fuse.S_IFREG | 0444)
	if o.IsDir() {
		mode = fuse.S_IFDIR | 0555
	}
	return fuse.Stat_t{Ino: f.ids.ID(namespace.NodeKey{Namespace: f.ns, ObjectID: o.ID}), Mode: mode, Nlink: 1, Size: max(0, o.Size), Mtim: fuse.NewTimespec(o.Modified), Ctim: fuse.NewTimespec(o.Modified), Atim: fuse.NewTimespec(o.Modified)}
}
func (f *filesystem) Read(_ string, b []byte, off int64, id uint64) int {
	h := f.get(id)
	if h == nil {
		return -fuse.EBADF
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return -fuse.EBADF
	}
	n, err := h.reader.ReadAt(f.ctx, b, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return errno(err)
	}
	return n
}
func (f *filesystem) Release(_ string, id uint64) int {
	f.mu.Lock()
	h := f.handles[id]
	delete(f.handles, id)
	f.mu.Unlock()
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return errno(h.reader.Close())
}
func (f *filesystem) Opendir(path string) (int, uint64) {
	o, err := f.resolve(path)
	if err != nil {
		return errno(err), 0
	}
	if !o.IsDir() {
		return -fuse.ENOTDIR, 0
	}
	return 0, 0
}
func (f *filesystem) Releasedir(string, uint64) int { return 0 }
func (f *filesystem) Readdir(path string, fill func(string, *fuse.Stat_t, int64) bool, offset int64, _ uint64) int {
	o, err := f.resolve(path)
	if err != nil {
		return errno(err)
	}
	objects, err := f.session.List(f.ctx, o)
	if err != nil {
		return errno(err)
	}
	names := make(map[string]bool, len(objects))
	for _, o := range objects {
		key := strings.ToLower(o.Name)
		if names[key] {
			return -fuse.EIO
		}
		names[key] = true
	}
	for i, o := range objects {
		if int64(i) < offset {
			continue
		}
		a := f.attrs(o)
		if !fill(o.Name, &a, int64(i+1)) {
			break
		}
	}
	return 0
}
func (f *filesystem) Access(path string, mask uint32) int {
	if mask&2 != 0 {
		return -fuse.EROFS
	}
	_, err := f.resolve(path)
	return errno(err)
}
func (f *filesystem) Flush(string, uint64) int       { return 0 }
func (f *filesystem) Fsync(string, bool, uint64) int { return 0 }
func (f *filesystem) Destroy() {
	f.mu.Lock()
	ids := make([]uint64, 0, len(f.handles))
	for id := range f.handles {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, id := range ids {
		f.Release("", id)
	}
}
func errno(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, provider.ErrNotFound):
		return -fuse.ENOENT
	case errors.Is(err, provider.ErrPermission):
		return -fuse.EACCES
	case errors.Is(err, provider.ErrConflict):
		return -fuse.EIO
	case errors.Is(err, provider.ErrNotDirectory):
		return -fuse.ENOTDIR
	case errors.Is(err, provider.ErrUnsupported):
		return -fuse.ENOSYS
	case errors.Is(err, context.Canceled):
		return -fuse.EINTR
	}
	return -fuse.EIO
}
func Mount(ctx context.Context, s provider.Session, path, name string, debug bool, ready func()) error {
	if err := Preflight(path); err != nil {
		return err
	}
	f := &filesystem{session: s, ctx: ctx, handles: map[uint64]*handle{}, ready: make(chan struct{})}
	if n, ok := s.(interface{ Namespace() namespace.Namespace }); ok {
		f.ns = n.Namespace()
	}
	host := fuse.NewFileSystemHost(f)
	host.SetCapReaddirPlus(true)
	args := []string{"-o", "ro,use_ino,volname=" + name}
	if debug {
		args = append(args, "-d")
	}
	done := make(chan bool, 1)
	go func() { done <- host.Mount(path, args) }()
	select {
	case ok := <-done:
		if !ok {
			return errors.New("WinFsp mount failed")
		}
		return nil
	case <-f.ready:
	case <-ctx.Done():
		select {
		case <-f.ready:
		case <-done:
			return nil
		}
	}
	if ready != nil {
		ready()
	}
	select {
	case ok := <-done:
		if !ok {
			return errors.New("WinFsp stopped with an error")
		}
		return nil
	case <-ctx.Done():
		if !host.Unmount() {
			return errors.New("WinFsp unmount failed")
		}
		<-done
		return nil
	}
}
