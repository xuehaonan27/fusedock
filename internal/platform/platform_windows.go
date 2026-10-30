package platform

import (
	"context"
	"errors"
	"github.com/xuehaonan27/fusedock/internal/driver/winfsp"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func Mount(ctx context.Context, s provider.Session, path string, o Options) error {
	if !o.ReadOnly {
		return errors.New("only read-only mounts are supported")
	}
	name := o.VolumeName
	if name == "" {
		name = "FuseDock"
	}
	return winfsp.Mount(ctx, s, path, name, o.Debug, o.Ready)
}
func Preflight(path string) error { return winfsp.Preflight(path) }
