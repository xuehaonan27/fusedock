//go:build linux

package linux_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	native "github.com/xuehaonan27/fusedock/internal/driver/linux"
	"github.com/xuehaonan27/fusedock/internal/providers/local"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func TestLocalProviderMount(t *testing.T) {
	if os.Getenv("FUSEDOCK_FUSE_TEST") != "1" {
		t.Skip("set FUSEDOCK_FUSE_TEST=1 on a Linux host with FUSE 3 to run native mount acceptance")
	}
	root := t.TempDir()
	mountpoint := t.TempDir()
	want := []byte("mount acceptance through a local provider\n")
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), want, 0600); err != nil {
		t.Fatal(err)
	}
	session, err := local.Factory{}.Open(context.Background(), provider.OpenRequest{
		Options: map[string]string{"root": root},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- native.Mount(ctx, session, mountpoint, native.Options{ReadOnly: true}) }()
	deadline := time.Now().Add(5 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, err = os.ReadFile(filepath.Join(mountpoint, "hello.txt"))
		if err == nil || len(got) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		cancel()
		<-done
		t.Fatalf("read mounted file: %v", err)
	}
	if string(got) != string(want) {
		cancel()
		<-done
		t.Fatalf("mounted content %q, want %q", got, want)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
