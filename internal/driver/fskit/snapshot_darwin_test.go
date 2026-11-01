package fskit_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	native "github.com/xuehaonan27/fusedock/internal/driver/fskit"
	"github.com/xuehaonan27/fusedock/internal/nativetest"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"golang.org/x/sys/unix"
)

func TestFSKitSnapshotContract(t *testing.T) {
	if os.Getenv("FUSEDOCK_FSKIT_TEST") != "1" {
		t.Skip("requires installed and enabled FSKit extension")
	}
	if macOSMajor(t) < 27 {
		t.Skip("active kernel cache invalidation requires macOS 27; macOS 26 uses the remount contract")
	}
	nativetest.Run(t, func(ctx context.Context, s provider.Session, path string, ready func()) error {
		return native.Mount(ctx, s, path, native.Options{ReadOnly: true, Ready: ready})
	})
}

func TestFSKitCompatibilityContract(t *testing.T) {
	if os.Getenv("FUSEDOCK_FSKIT_TEST") != "1" {
		t.Skip("requires installed and enabled FSKit extension")
	}
	if macOSMajor(t) != 26 {
		t.Skip("requires the macOS 26 compatibility lifecycle")
	}
	nativetest.RunRemountRefresh(t, func(ctx context.Context, s provider.Session, path string, ready func()) error {
		return native.Mount(ctx, s, path, native.Options{ReadOnly: true, Ready: ready})
	})
}

func macOSMajor(t *testing.T) int {
	t.Helper()
	version, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		t.Fatal(err)
	}
	var major int
	if _, err := fmt.Sscanf(version, "%d.", &major); err != nil {
		t.Fatal(err)
	}
	return major
}
