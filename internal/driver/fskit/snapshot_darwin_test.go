package fskit_test

import (
	"context"
	native "github.com/xuehaonan27/fusedock/internal/driver/fskit"
	"github.com/xuehaonan27/fusedock/internal/nativetest"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"os"
	"testing"
)

func TestFSKitSnapshotContract(t *testing.T) {
	if os.Getenv("FUSEDOCK_FSKIT_TEST") != "1" {
		t.Skip("requires installed and enabled FSKit extension")
	}
	nativetest.Run(t, func(ctx context.Context, s provider.Session, path string, ready func()) error {
		return native.Mount(ctx, s, path, native.Options{ReadOnly: true, Ready: ready})
	})
}
