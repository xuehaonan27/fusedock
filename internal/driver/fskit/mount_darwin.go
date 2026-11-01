//go:build darwin

package fskit

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/xuehaonan27/fusedock/internal/broker"
	"github.com/xuehaonan27/fusedock/internal/credentials"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func Preflight(path string) error {
	version, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return err
	}
	var major int
	if _, err := fmt.Sscanf(version, "%d.", &major); err != nil {
		return err
	}
	if major < 26 {
		return fmt.Errorf("FuseDock FSKit requires macOS 26 or later; this host runs %s", version)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("mountpoint must be an empty directory")
	}
	return nil
}
func Mount(ctx context.Context, s provider.Session, path string, o Options) error {
	if !o.ReadOnly {
		return errors.New("only read-only mounts are supported")
	}
	if err := Preflight(path); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "fusedock-mount-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	server, err := broker.NewServer(s, token)
	if err != nil {
		return err
	}
	if o.ReadSlots != nil {
		server.SetReadSlots(o.ReadSlots)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	config := filepath.Join(dir, "mount.credentials")
	values := map[string][]byte{"provider": []byte("broker"), "endpoint": []byte("http://" + listener.Addr().String()), "token": []byte(token)}
	values["entry_ttl"] = []byte(o.EntryTTL.String())
	if volume, ok := s.(interface{ VolumeID() string }); ok {
		values["volume_id"] = []byte(volume.VolumeID())
	}
	if err := credentials.WritePrivate(config, values); err != nil {
		return err
	}
	server.SetOnClaim(func() { os.Remove(config) })
	serveCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.SetOnClose(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(serveCtx, listener) }()
	if out, err := exec.CommandContext(ctx, "/sbin/mount", "-t", "fusedock", config, path).CombinedOutput(); err != nil {
		return fmt.Errorf("FSKit mount: %w: %s", err, out)
	}
	if o.Ready != nil {
		o.Ready()
	}
	select {
	case err := <-done:
		exec.Command("/sbin/umount", path).Run()
		return err
	case <-ctx.Done():
	}
	if out, err := exec.Command("/sbin/umount", path).CombinedOutput(); err != nil {
		return fmt.Errorf("FSKit unmount: %w: %s", err, out)
	}
	cancel()
	return <-done
}
