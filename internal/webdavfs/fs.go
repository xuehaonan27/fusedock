// Package webdavfs exposes a read-only provider session as a WebDAV file
// system. The provider namespace is the only source of identity; WebDAV paths
// are resolved through List and are never used for local filesystem access.
package webdavfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	dav "golang.org/x/net/webdav"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

type FileSystem struct {
	session provider.Session
	root    provider.Object
}

// New resolves scope once and confines all WebDAV paths beneath that object.
func New(ctx context.Context, session provider.Session, scope string) (*FileSystem, error) {
	if session == nil {
		return nil, errors.New("WebDAV provider session is required")
	}
	root, err := session.Root(ctx)
	if err != nil {
		return nil, err
	}
	parts, err := provider.CleanRelativePath(scope)
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		if !root.IsDir() {
			return nil, provider.ErrNotDirectory
		}
		objects, err := session.List(ctx, root)
		if err != nil {
			return nil, err
		}
		found := false
		for _, object := range objects {
			if object.Name == part {
				root, found = object, true
				break
			}
		}
		if !found {
			return nil, provider.ErrNotFound
		}
	}
	if !root.IsDir() {
		return nil, provider.ErrNotDirectory
	}
	return &FileSystem{session: session, root: root}, nil
}

func (fs *FileSystem) Root() provider.Object { return fs.root }

func (fs *FileSystem) Mkdir(context.Context, string, os.FileMode) error {
	return os.ErrPermission
}

func (fs *FileSystem) RemoveAll(context.Context, string) error {
	return os.ErrPermission
}

func (fs *FileSystem) Rename(context.Context, string, string) error {
	return os.ErrPermission
}

func (fs *FileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	object, err := fs.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	return fileInfo{object: object}, nil
}

func (fs *FileSystem) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (dav.File, error) {
	if flag&3 != os.O_RDONLY || flag&(os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		return nil, os.ErrPermission
	}
	object, err := fs.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	if object.IsDir() {
		objects, err := fs.session.List(ctx, object)
		if err != nil {
			return nil, mapError(err)
		}
		sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })
		return &directory{object: object, objects: objects}, nil
	}
	reader, err := fs.session.OpenRead(ctx, object)
	if err != nil {
		return nil, mapError(err)
	}
	return &file{object: object, reader: reader, size: object.Size}, nil
}

func (fs *FileSystem) resolve(ctx context.Context, name string) (provider.Object, error) {
	parts, err := provider.CleanRelativePath(name)
	if err != nil {
		return provider.Object{}, os.ErrPermission
	}
	current := fs.root
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return provider.Object{}, err
		}
		if !current.IsDir() {
			return provider.Object{}, os.ErrNotExist
		}
		objects, err := fs.session.List(ctx, current)
		if err != nil {
			return provider.Object{}, mapError(err)
		}
		found := false
		for _, object := range objects {
			if object.Name == part {
				current, found = object, true
				break
			}
		}
		if !found {
			return provider.Object{}, os.ErrNotExist
		}
	}
	return current, nil
}

type fileInfo struct {
	object provider.Object
}

func (info fileInfo) Name() string { return info.object.Name }
func (info fileInfo) Size() int64 {
	if info.object.IsDir() {
		return 0
	}
	return max(info.object.Size, 0)
}
func (info fileInfo) Mode() os.FileMode {
	if info.object.IsDir() {
		return os.ModeDir | 0o555
	}
	return 0o444
}
func (info fileInfo) ModTime() time.Time {
	if info.object.Modified.IsZero() {
		return time.Unix(0, 0)
	}
	return info.object.Modified
}
func (info fileInfo) IsDir() bool { return info.object.IsDir() }
func (info fileInfo) Sys() any    { return info.object }
func (info fileInfo) ContentType(context.Context) (string, error) {
	if info.object.IsDir() {
		return "httpd/unix-directory", nil
	}
	if contentType := mime.TypeByExtension(filepath.Ext(info.object.Name)); contentType != "" {
		return contentType, nil
	}
	return "application/octet-stream", nil
}
func (info fileInfo) ETag(context.Context) (string, error) {
	digest := sha256.Sum256([]byte(info.object.ID + "\x00" + info.object.Version))
	return `"` + hex.EncodeToString(digest[:16]) + `"`, nil
}

type directory struct {
	object  provider.Object
	objects []provider.Object
	mu      sync.Mutex
	offset  int
	closed  bool
}

func (d *directory) Readdir(count int) ([]os.FileInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, os.ErrClosed
	}
	if d.offset >= len(d.objects) {
		return nil, io.EOF
	}
	end := len(d.objects)
	if count > 0 && d.offset+count < end {
		end = d.offset + count
	}
	result := make([]os.FileInfo, 0, end-d.offset)
	for _, object := range d.objects[d.offset:end] {
		result = append(result, fileInfo{object: object})
	}
	d.offset = end
	if count > 0 && len(result) == 0 {
		return nil, io.EOF
	}
	return result, nil
}

func (d *directory) Stat() (os.FileInfo, error) { return fileInfo{object: d.object}, nil }
func (d *directory) Read([]byte) (int, error)   { return 0, os.ErrInvalid }
func (d *directory) Seek(int64, int) (int64, error) {
	return 0, os.ErrInvalid
}
func (d *directory) Write([]byte) (int, error) { return 0, os.ErrPermission }
func (d *directory) Close() error {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	return nil
}

type file struct {
	object provider.Object
	reader provider.Reader
	size   int64
	mu     sync.Mutex
	offset int64
	closed bool
}

func (f *file) Read(dest []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.offset >= f.size {
		return 0, io.EOF
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	n, err := f.reader.ReadAt(ctx, dest, f.offset)
	cancel()
	if n > 0 {
		f.offset += int64(n)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, mapError(err)
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (f *file) Readdir(int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }
func (f *file) Stat() (os.FileInfo, error)         { return fileInfo{object: f.object}, nil }
func (f *file) Write([]byte) (int, error)          { return 0, os.ErrPermission }
func (f *file) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.offset + offset
	case io.SeekEnd:
		next = f.size + offset
	default:
		return 0, os.ErrInvalid
	}
	if next < 0 {
		return 0, os.ErrInvalid
	}
	f.offset = next
	return next, nil
}
func (f *file) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	reader := f.reader
	f.reader = nil
	f.mu.Unlock()
	if reader == nil {
		return nil
	}
	return mapError(reader.Close())
}

func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, provider.ErrNotFound):
		return os.ErrNotExist
	case errors.Is(err, provider.ErrPermission):
		return os.ErrPermission
	case errors.Is(err, provider.ErrNotDirectory):
		return os.ErrInvalid
	case errors.Is(err, provider.ErrConflict):
		return os.ErrExist
	case errors.Is(err, provider.ErrUnsupported):
		return errors.New("WebDAV operation is unsupported by provider")
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return err
	}
}

var _ dav.FileSystem = (*FileSystem)(nil)
var _ dav.File = (*file)(nil)
var _ dav.File = (*directory)(nil)
