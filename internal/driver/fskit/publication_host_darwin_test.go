package fskit_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/xuehaonan27/fusedock/internal/broker"
	native "github.com/xuehaonan27/fusedock/internal/driver/fskit"
	client "github.com/xuehaonan27/fusedock/internal/providers/broker"
	"github.com/xuehaonan27/fusedock/internal/providers/local"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func TestPublicationCannotMutateAnUnmountedHostDirectory(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{"staged": "staged", "destination": "original"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base, err := local.New().Open(context.Background(), provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	const bootstrap = "0123456789abcdef0123456789abcdef"
	server, err := broker.NewServer(base, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if err := native.ConfigurePublication(server, root); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	session, err := client.New().Open(context.Background(), provider.OpenRequest{
		Options: map[string]string{"endpoint": host.URL}, Secrets: map[string][]byte{"token": []byte(bootstrap)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	publisher := session.(*client.Session)
	if !publisher.CanPublishNamespace() {
		t.Fatal("missing configured publication capability")
	}
	for _, destination := range []string{"destination", ""} {
		if err := publisher.PublishNamespace(context.Background(), "staged", destination, false); err == nil {
			t.Fatal("host publication accepted an ordinary directory")
		}
	}
	for name, expected := range map[string]string{"staged": "staged", "destination": "original"} {
		actual, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(actual) != expected {
			t.Fatalf("host file changed: %s %q %v", name, actual, err)
		}
	}
}
