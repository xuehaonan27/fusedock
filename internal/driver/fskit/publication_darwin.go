package fskit

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/xuehaonan27/fusedock/internal/broker"
	"golang.org/x/sys/unix"
)

// ConfigurePublication binds the publisher to the FSKit resource supplied by
// the host, never to a path supplied over RPC. The extension authorizes each
// rename against a single pending publication; ordinary writes still fail.
func ConfigurePublication(server *broker.Server, resource string) error {
	absolute, err := filepath.Abs(resource)
	if err != nil {
		return err
	}
	server.SetNamespacePublisher(func(ctx context.Context, request broker.PublicationRequest) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !request.Valid() {
			return errors.New("invalid publication names")
		}
		count, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
		if err != nil {
			return err
		}
		mounts := make([]unix.Statfs_t, count+1)
		count, err = unix.Getfsstat(mounts, unix.MNT_NOWAIT)
		if err != nil {
			return err
		}
		var target *unix.Statfs_t
		for i := range mounts[:count] {
			m := &mounts[i]
			if m.Owner != uint32(os.Getuid()) || cstring(m.Fstypename[:]) != "fusedock" {
				continue
			}
			source, err := url.Parse(cstring(m.Mntfromname[:]))
			if err != nil || source.Scheme != "file" || source.Host != "" || filepath.Clean(source.Path) != absolute {
				continue
			}
			if target != nil {
				return errors.New("ambiguous mounted resource")
			}
			target = m
		}
		if target == nil {
			return errors.New("resource is not mounted")
		}
		fd, err := unix.Open(cstring(target.Mntonname[:]), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		var actual unix.Statfs_t
		if err := unix.Fstatfs(fd, &actual); err != nil {
			return err
		}
		if actual.Fsid != target.Fsid || cstring(actual.Mntfromname[:]) != cstring(target.Mntfromname[:]) {
			return errors.New("mounted resource changed")
		}
		if request.Remove {
			flags := 0
			if request.Directory {
				flags = unix.AT_REMOVEDIR
			}
			return unix.Unlinkat(fd, request.From, flags)
		}
		return unix.Renameat(fd, request.From, fd, request.To)
	})
	return nil
}

func cstring(data []byte) string { return strings.TrimRight(string(data), "\x00") }
