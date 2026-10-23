// Package providers assembles the built-in provider registry. Third-party Go
// providers can register in the same way without changing the mount layer.
package providers

import (
	"fmt"

	"github.com/xuehaonan/fusedock/internal/providers/baidu"
	"github.com/xuehaonan/fusedock/internal/providers/broker"
	"github.com/xuehaonan/fusedock/internal/providers/local"
	"github.com/xuehaonan/fusedock/internal/providers/pku"
	"github.com/xuehaonan/fusedock/pkg/provider"
)

func Builtin() (*provider.Registry, error) {
	registry := provider.NewRegistry()
	for _, factory := range []provider.Factory{local.New(), pku.New(), baidu.New(), broker.New()} {
		if err := registry.Register(factory); err != nil {
			return nil, fmt.Errorf("register built-in provider: %w", err)
		}
	}
	return registry, nil
}
