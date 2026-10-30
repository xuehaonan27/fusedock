package blockcache

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/xuehaonan27/fusedock/internal/credentials"
)

const headerSize = 80

type diskEntry struct {
	path string
	size int64
	at   time.Time
}
type diskStore struct {
	policy       *diskPolicy
	mu           sync.Mutex
	root         *os.Root
	path         string
	lock         *flock.Flock
	limit, bytes int64
	entries      map[string]diskEntry
	epochs       map[string]uint64
	ready        chan struct{}
	metrics      *Metrics
}

func openDisk(path string, limit int64, m *Metrics) (*diskStore, error) {
	if err := credentials.EnsurePrivateParent(filepath.Join(path, "owner.lock")); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("cache root is not a directory")
	}
	if err := os.Chmod(path, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(path, "owner.lock"))
	ok, err := lock.TryLock()
	if err != nil || !ok {
		root.Close()
		if err == nil {
			err = errors.New("cache directory has another owner")
		}
		return nil, err
	}
	d := &diskStore{policy: newDiskPolicy(limit), root: root, path: path, lock: lock, limit: limit, entries: map[string]diskEntry{}, epochs: map[string]uint64{}, ready: make(chan struct{}), metrics: m}
	for _, name := range []string{"entries", "tmp"} {
		if err := root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			d.closeRoot()
			return nil, err
		}
	}
	go d.scan()
	return d, nil
}
func (d *diskStore) scan() {
	defer close(d.ready)
	_ = fs.WalkDir(d.root.FS(), "entries", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".blk") {
			return nil
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		d.mu.Lock()
		if _, exists := d.entries[path]; !exists {
			d.entries[path] = diskEntry{path: path, size: info.Size(), at: info.ModTime()}
			d.bytes += info.Size()
			d.policy.add(path, info.Size())
		}
		d.mu.Unlock()
		return nil
	})
	d.mu.Lock()
	entries := make([]diskEntry, 0, len(d.entries))
	for _, e := range d.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	for _, e := range entries {
		n := d.policy.nodes[e.path]
		if n != nil && !n.protected {
			d.policy.recent.MoveToFront(n.elem)
		}
	}
	d.mu.Unlock()
	temp, _ := fs.ReadDir(d.root.FS(), "tmp")
	for _, e := range temp {
		if !e.IsDir() {
			_ = d.root.Remove("tmp/" + e.Name())
		}
	}
}
func blockPath(k Key) string {
	id := k.Hash()
	return "entries/" + k.Node.Namespace.Tag() + "/" + id[:2] + "/" + id + ".blk"
}
func (d *diskStore) read(k Key, dest []byte, access uint64) bool {
	path := blockPath(k)
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := d.root.OpenFile(path, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !validFile(f, info) || info.Size() != headerSize+int64(len(dest)) {
		f.Close()
		d.corrupt(path)
		return false
	}
	var h [headerSize]byte
	if _, err = io.ReadFull(f, h[:]); err != nil {
		f.Close()
		d.corrupt(path)
		return false
	}
	key, _ := hex.DecodeString(k.Hash())
	if string(h[:4]) != "FDBK" || binary.BigEndian.Uint16(h[4:6]) != 1 || binary.BigEndian.Uint16(h[6:8]) != headerSize || binary.BigEndian.Uint64(h[8:16]) != uint64(len(dest)) || string(h[16:48]) != string(key) {
		f.Close()
		d.corrupt(path)
		return false
	}
	if _, err = io.ReadFull(f, dest); err != nil {
		f.Close()
		d.corrupt(path)
		return false
	}
	hash := sha256.Sum256(dest)
	if string(hash[:]) != string(h[48:80]) {
		f.Close()
		d.corrupt(path)
		return false
	}
	// Disk fills verify once while materializing the memory entry; subsequent
	// small reads use the immutable shared memory block.
	now := time.Now()
	old, exists := d.entries[path]
	if !exists {
		d.bytes += info.Size()
		d.policy.add(path, info.Size())
	}
	d.entries[path] = diskEntry{path: path, size: info.Size(), at: now}
	if !exists || now.Sub(old.at) > time.Minute {
		_ = d.root.Chtimes(path, now, now)
	}
	d.policy.touch(path, access)
	d.metrics.DiskHits.Add(1)
	return true
}
func (d *diskStore) corrupt(path string) { d.metrics.Corrupt.Add(1); d.remove(path) }
func (d *diskStore) remove(path string) error {
	if err := d.root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if old, ok := d.entries[path]; ok {
		d.bytes -= old.size
		delete(d.entries, path)
		d.policy.remove(path)
	}
	return nil
}
func (d *diskStore) write(k Key, data []byte, epoch uint64, access uint64) {
	select {
	case <-d.ready:
	default:
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.epochs[k.Node.Namespace.Tag()] != epoch {
		return
	}
	size := int64(headerSize + len(data))
	if size > d.limit {
		return
	}
	path := blockPath(k)
	if _, ok := d.entries[path]; ok {
		return
	}
	for d.bytes+size > d.limit {
		victim := d.policy.victim()
		if victim == "" {
			return
		}
		before := d.bytes
		d.remove(victim)
		if d.bytes == before {
			d.metrics.WriteFailures.Add(1)
			return
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		d.metrics.WriteFailures.Add(1)
		return
	}
	tmp := "tmp/" + hex.EncodeToString(random[:])
	f, err := d.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, 0600)
	if err != nil {
		d.metrics.WriteFailures.Add(1)
		return
	}
	defer d.root.Remove(tmp)
	var h [headerSize]byte
	copy(h[:4], "FDBK")
	binary.BigEndian.PutUint16(h[4:6], 1)
	binary.BigEndian.PutUint16(h[6:8], headerSize)
	binary.BigEndian.PutUint64(h[8:16], uint64(len(data)))
	key, _ := hex.DecodeString(k.Hash())
	copy(h[16:48], key)
	hash := sha256.Sum256(data)
	copy(h[48:], hash[:])
	_, err = f.Write(h[:])
	if err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	directory := filepath.ToSlash(filepath.Dir(path))
	current := ""
	for _, part := range strings.Split(directory, "/") {
		if current != "" {
			current += "/"
		}
		current += part
		if e := d.root.Mkdir(current, 0700); e != nil && !errors.Is(e, os.ErrExist) {
			err = e
			break
		}
	}
	if err == nil {
		err = d.root.Rename(tmp, path)
	}
	if err != nil {
		d.metrics.WriteFailures.Add(1)
		return
	}
	d.entries[path] = diskEntry{path: path, size: size, at: time.Now()}
	d.bytes += size
	d.policy.add(path, size)
	d.policy.touch(path, access)
}
func (d *diskStore) usage() int64 { d.mu.Lock(); defer d.mu.Unlock(); return d.bytes }
func (d *diskStore) clear(tag string, epoch uint64) error {
	<-d.ready
	d.mu.Lock()
	defer d.mu.Unlock()
	d.epochs[tag] = epoch
	var result error
	for path := range d.entries {
		if strings.HasPrefix(path, "entries/"+tag+"/") {
			result = errors.Join(result, d.remove(path))
		}
	}
	return result
}
func (d *diskStore) closeRoot() error { return errors.Join(d.root.Close(), d.lock.Close()) }
func (d *diskStore) close() error     { <-d.ready; return d.closeRoot() }
func (c *Cache) Verify() (int, error) {
	if c.disk == nil {
		return 0, nil
	}
	d := c.disk
	<-d.ready
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for path, e := range d.entries {
		f, err := d.root.OpenFile(path, os.O_RDONLY|noFollow, 0)
		if err != nil {
			return count, err
		}
		info, err := f.Stat()
		if err != nil || !validFile(f, info) {
			f.Close()
			d.corrupt(path)
			return count, errors.New("invalid cache entry file")
		}
		var h [headerSize]byte
		_, err = io.ReadFull(f, h[:])
		sum := sha256.New()
		var n int64
		if err == nil {
			n, err = io.Copy(sum, io.LimitReader(f, d.limit+1))
		}
		f.Close()
		expected := strings.TrimSuffix(filepath.Base(path), ".blk")
		if err != nil || e.size != n+headerSize || string(h[:4]) != "FDBK" || binary.BigEndian.Uint16(h[4:6]) != 1 || binary.BigEndian.Uint16(h[6:8]) != headerSize || hex.EncodeToString(h[16:48]) != expected || binary.BigEndian.Uint64(h[8:16]) != uint64(n) || string(sum.Sum(nil)) != string(h[48:]) {
			d.corrupt(path)
			return count, fmt.Errorf("corrupt cache entry %s", expected)
		}
		count++
	}
	return count, nil
}

// Prune evicts unpinned retained data and bounds persistent usage to bytes.
func (c *Cache) Prune(bytes int64) error {
	if bytes < 0 {
		return errors.New("prune budget must be nonnegative")
	}
	c.mu.Lock()
	for c.used > bytes && c.evict() {
	}
	c.signal()
	c.mu.Unlock()
	if c.disk == nil {
		return nil
	}
	d := c.disk
	<-d.ready
	d.mu.Lock()
	defer d.mu.Unlock()
	for d.bytes > bytes {
		victim := d.policy.victim()
		if victim == "" {
			break
		}
		before := d.bytes
		d.remove(victim)
		if d.bytes == before {
			return errors.New("cannot remove cache entry")
		}
	}
	return nil
}

func (d *diskStore) touch(k Key, access uint64) {
	d.mu.Lock()
	d.policy.touch(blockPath(k), access)
	d.mu.Unlock()
}
