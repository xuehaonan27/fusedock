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
	"time"

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
	if mounted, err := mountAt(path); err != nil {
		return err
	} else if mounted != nil {
		return fmt.Errorf("mountpoint already has a mounted filesystem: %s", path)
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
	if err := ConfigurePublication(server, config); err != nil {
		return err
	}
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
		cancel()
		<-done
		return fmt.Errorf("FSKit mount: %w: %s", err, out)
	}
	identity, err := mountAt(path)
	if err != nil || identity == nil {
		cancel()
		<-done
		if err != nil {
			return err
		}
		return errors.New("FSKit mount disappeared before readiness")
	}
	native := &nativeMount{path: path, identity: *identity}
	if o.RegisterUnmount != nil {
		o.RegisterUnmount(native.unmount)
	}
	if o.Ready != nil {
		o.Ready()
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return errors.Join(err, native.unmount(context.Background()))
		case <-ticker.C:
			present, err := native.present()
			if err != nil {
				continue
			}
			if !present {
				cancel()
				return <-done
			}
		case <-ctx.Done():
			err := native.unmount(context.Background())
			cancel()
			return errors.Join(err, <-done)
		}
	}
}
