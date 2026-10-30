package winfsp_test

import (
	"context"
	"github.com/xuehaonan27/fusedock/internal/nativetest"
	"github.com/xuehaonan27/fusedock/internal/platform"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"os"
	"testing"
)

func TestWinFspSnapshotContract(t *testing.T) {
	if os.Getenv("FUSEDOCK_WINFSP_TEST") != "1" {
		t.Skip("requires Windows WinFsp host")
	}
	nativetest.Run(t, func(ctx context.Context, s provider.Session, path string, ready func()) error {
		return platform.Mount(ctx, s, path, platform.Options{ReadOnly: true, Ready: ready})
	})
}
