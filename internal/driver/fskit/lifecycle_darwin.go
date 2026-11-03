package fskit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Read the kernel mount table without invoking filesystem/provider operations.
func mountAt(path string) (*unix.Statfs_t, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	path = filepath.Join(parent, filepath.Base(path))
	count, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	mounts := make([]unix.Statfs_t, count+1)
	count, err = unix.Getfsstat(mounts, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	for i := range mounts[:count] {
		if filepath.Clean(cstring(mounts[i].Mntonname[:])) == filepath.Clean(path) {
			return &mounts[i], nil
		}
	}
	return nil, nil
}

type nativeMount struct {
	mu       sync.Mutex
	path     string
	identity unix.Statfs_t
}

func (m *nativeMount) present() (bool, error) {
	current, err := mountAt(m.path)
	if err != nil || current == nil {
		return false, err
	}
	return current.Fsid == m.identity.Fsid && current.Owner == uint32(os.Getuid()) && cstring(current.Mntfromname[:]) == cstring(m.identity.Mntfromname[:]), nil
}

// Disk Arbitration asks Finder and other clients to release the volume. A busy
// result leaves both the native mount and the broker available for retry.
func (m *nativeMount) unmount(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	present, err := m.present()
	if err != nil || !present {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/sbin/diskutil", "unmount", m.path)
	command.WaitDelay = time.Second
	out, err := command.CombinedOutput()
	if err != nil {
		if present, checkErr := m.present(); checkErr == nil && !present {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("FSKit unmount: %w", ctx.Err())
		}
		return fmt.Errorf("FSKit unmount: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
