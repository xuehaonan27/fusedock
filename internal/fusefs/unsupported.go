//go:build windows

package fusefs

import (
	"context"
	"errors"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

func Mount(context.Context, provider.Session, string, Options) error {
	return errors.New("filesystem mounting is not implemented on this platform")
}

func Preflight(string) error {
	return errors.New("WinFsp mounting is not implemented yet")
}
