//go:build !darwin

package platform

import "github.com/xuehaonan27/fusedock/internal/broker"

func ConfigureBroker(*broker.Server, string) error { return nil }
