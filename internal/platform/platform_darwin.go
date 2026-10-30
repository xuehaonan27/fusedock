package platform

import (
	"context"
	native "github.com/xuehaonan27/fusedock/internal/driver/fskit"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func Mount(ctx context.Context, session provider.Session, path string, options Options) error {
	return native.Mount(ctx, session, path, options)
}
func Preflight(path string) error { return native.Preflight(path) }
