package platform

import (
	"github.com/xuehaonan27/fusedock/internal/broker"
	"github.com/xuehaonan27/fusedock/internal/driver/fskit"
)

func ConfigureBroker(server *broker.Server, resource string) error {
	return fskit.ConfigurePublication(server, resource)
}
