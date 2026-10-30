package linux_test

import (
	"context"
	native "github.com/xuehaonan27/fusedock/internal/driver/linux"
	"github.com/xuehaonan27/fusedock/internal/nativetest"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"os"
	"testing"
	"time"
)

func TestLinuxSnapshotContract(t *testing.T) {
	if os.Getenv("FUSEDOCK_FUSE_TEST") != "1" {
		t.Skip("requires Linux FUSE host")
	}
	nativetest.Run(t, func(ctx context.Context, s provider.Session, path string, ready func()) error {
		return native.Mount(ctx, s, path, native.Options{ReadOnly: true, AttrTTL: time.Millisecond, EntryTTL: time.Millisecond, Ready: ready})
	})
}
