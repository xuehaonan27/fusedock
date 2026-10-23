package broker

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	brokerapi "github.com/xuehaonan/fusedock/internal/broker"
	"github.com/xuehaonan/fusedock/internal/providers/local"
	"github.com/xuehaonan/fusedock/pkg/provider"
)

const bootstrap = "0123456789abcdef0123456789abcdef"

func TestBrokerProviderEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("remote broker data"), 0600); err != nil {
		t.Fatal(err)
	}
	localSession, err := local.Factory{}.Open(ctx, provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	defer localSession.Close()
	server, err := brokerapi.NewServer(localSession, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(chan struct{}, 1)
	server.SetOnClaim(func() { claimed <- struct{}{} })
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	endpoint := "http://" + listener.Addr().String()

	session, err := Factory{}.Open(ctx, provider.OpenRequest{
		Options: map[string]string{"endpoint": endpoint},
		Secrets: map[string][]byte{"token": []byte(bootstrap)},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-claimed:
	case <-time.After(time.Second):
		t.Fatal("claim callback was not invoked")
	}
	rootObject, err := session.Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := session.List(ctx, rootObject)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Name != "hello.txt" {
		t.Fatalf("unexpected listing: %+v", objects)
	}
	object, err := session.Stat(ctx, objects[0])
	if err != nil {
		t.Fatal(err)
	}
	reader, err := session.OpenRead(ctx, object)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 6)
	n, err := reader.ReadAt(ctx, buffer, 7)
	if err != nil || n != len(buffer) || string(buffer) != "broker" {
		t.Fatalf("read=(%d,%v) %q", n, err, buffer)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stat(ctx, provider.Object{ID: "missing", Name: "missing", Kind: provider.KindFile}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("missing object error = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (Factory{}).Open(ctx, provider.OpenRequest{
		Options: map[string]string{"endpoint": endpoint},
		Secrets: map[string][]byte{"token": []byte(bootstrap)},
	}); err == nil {
		t.Fatal("bootstrap capability was reusable")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("broker did not stop")
	}
}

func TestBrokerProviderValidatesEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://0.0.0.0:1", "https://127.0.0.1:1", "http://example.test:1",
		"http://127.0.0.1", "http://127.0.0.1:1/path", "http://user@127.0.0.1:1",
	} {
		if _, err := (Factory{}).Open(context.Background(), provider.OpenRequest{
			Options: map[string]string{"endpoint": endpoint},
			Secrets: map[string][]byte{"token": []byte(bootstrap)},
		}); err == nil {
			t.Errorf("accepted endpoint %q", endpoint)
		}
	}
}

func TestBrokerReaderRejectsClosedHandle(t *testing.T) {
	r := &reader{session: &Session{}, object: provider.Object{ID: "file", Name: "file", Kind: provider.KindFile, Size: 1}}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := r.ReadAt(context.Background(), make([]byte, 1), 0); n != 0 || err == nil {
		t.Fatalf("read after close = (%d,%v)", n, err)
	}
}

var _ io.Closer = (*reader)(nil)
