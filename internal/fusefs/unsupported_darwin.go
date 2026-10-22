//go:build darwin

package fusefs

import (
	"context"
	"errors"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

const fskitInstruction = "macOS uses the native FSKit extension. Build it with `DEVELOPMENT_TEAM=<team-id> make macos-fskit`, install FuseDock.app, enable its File System Extension in System Settings, then mount with `mount -t fusedock ~/.config/fusedock/pku.credentials ~/FuseDockPKU`."

func Mount(context.Context, provider.Session, string, Options) error {
	return errors.New(fskitInstruction)
}

func Preflight(string) error {
	return errors.New(fskitInstruction)
}
