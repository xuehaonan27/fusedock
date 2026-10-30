//go:build linux || darwin

package localtransport

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/xuehaonan27/fusedock/internal/credentials"
	"net"
	"os"
	"path/filepath"
)

func Endpoint(dir string) string {
	h := sha256.Sum256([]byte(dir))
	return filepath.Join("/tmp", fmt.Sprint("fusedock-", os.Getuid()), fmt.Sprintf("%x.sock", h[:16]))
}

type listener struct{ *net.UnixListener }

func (l listener) Accept() (net.Conn, error) {
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		if sameUser(c) {
			return c, nil
		}
		c.Close()
	}
}
func Listen(dir string) (net.Listener, error) {
	path := Endpoint(dir)
	if err := credentials.EnsurePrivateParent(path); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		c, err := net.Dial("unix", path)
		if err == nil {
			c.Close()
			return nil, errors.New("daemon is already running")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		l.Close()
		return nil, err
	}
	return listener{l}, nil
}
func Dial(ctx context.Context, dir string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", Endpoint(dir))
	if err != nil {
		return nil, err
	}
	if !sameUser(c.(*net.UnixConn)) {
		c.Close()
		return nil, errors.New("daemon peer belongs to another user")
	}
	return c, nil
}
