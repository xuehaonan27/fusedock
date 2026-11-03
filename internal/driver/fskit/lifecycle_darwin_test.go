package fskit_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	native "github.com/xuehaonan27/fusedock/internal/driver/fskit"
	"github.com/xuehaonan27/fusedock/internal/providers/local"
	"github.com/xuehaonan27/fusedock/internal/vfs"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func TestFSKitBusyRetryAndExternalEject(t *testing.T) {
	if os.Getenv("FUSEDOCK_FSKIT_TEST") != "1" {
		t.Skip("requires installed and enabled FSKit extension")
	}
	root, point := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("still readable"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"busy-retry", "external-eject", "remount-after-eject"} {
		if !t.Run(mode, func(t *testing.T) {
			raw, err := local.New().Open(context.Background(), provider.OpenRequest{Options: map[string]string{"root": root}})
			if err != nil {
				t.Fatal(err)
			}
			session := vfs.New(raw, 0)
			defer session.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready, done := make(chan struct{}), make(chan error, 1)
			var unmount func(context.Context) error
			go func() {
				done <- native.Mount(ctx, session, point, native.Options{ReadOnly: true, RegisterUnmount: func(fn func(context.Context) error) { unmount = fn }, Ready: func() { close(ready) }})
			}()
			finished := false
			defer func() {
				if !finished {
					exec.Command("/usr/sbin/diskutil", "unmount", "force", point).Run()
					cancel()
					select {
					case <-done:
					case <-time.After(20 * time.Second):
						t.Error("mount worker did not exit")
					}
				}
			}()
			select {
			case <-ready:
			case err := <-done:
				finished = true
				t.Fatalf("mount: %v", err)
			case <-time.After(20 * time.Second):
				t.Fatal("mount readiness timeout")
			}
			if mode == "busy-retry" {
				hold, err := os.Open(filepath.Join(point, "file.txt"))
				if err != nil {
					t.Fatal(err)
				}
				defer hold.Close()
				started := time.Now()
				err = unmount(context.Background())
				t.Logf("busy unmount after %s: %v", time.Since(started), err)
				if err == nil {
					t.Fatal("open file did not make volume busy")
				}
				select {
				case err := <-done:
					finished = true
					t.Fatalf("busy unmount stopped broker: %v", err)
				default:
				}
				b := make([]byte, 14)
				if n, err := hold.ReadAt(b, 0); err != nil || n != 14 || string(b) != "still readable" {
					t.Fatalf("busy mount lost reads: %q %v", b, err)
				}
				hold.Close()
				if err := unmount(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else if mode == "external-eject" {
				// Bypass the registered callback, as Finder or diskutil would.
				if out, err := exec.Command("/usr/sbin/diskutil", "unmount", "force", point).CombinedOutput(); err != nil {
					t.Fatalf("external eject: %v %s", err, out)
				}
			} else {
				if err := unmount(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				finished = true
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("unmounted volume retained mount worker")
			}
		}) {
			return
		}
	}
}
