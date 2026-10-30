//go:build linux

package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

func TestMountRejectsUnrequestedWritableMode(t *testing.T) {
	err := Mount(context.Background(), &dummySession{}, t.TempDir(), Options{})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountErrorAddsDriverHint(t *testing.T) {
	err := mountError(errors.New("no FUSE mount utility found"))
	if !strings.Contains(err.Error(), "FUSE") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMountpoint(t *testing.T) {
	empty := t.TempDir()
	point, err := ValidateMountpoint(empty)
	if err != nil || point == "" {
		t.Fatalf("empty mountpoint rejected: %v", err)
	}
	nonempty := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonempty, "file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateMountpoint(nonempty); err == nil {
		t.Fatal("nonempty mountpoint was accepted")
	}
}

type dummySession struct{}

func (*dummySession) Root(context.Context) (provider.Object, error) { return provider.Object{}, nil }
func (*dummySession) Stat(context.Context, provider.Object) (provider.Object, error) {
	return provider.Object{}, nil
}
func (*dummySession) List(context.Context, provider.Object) ([]provider.Object, error) {
	return nil, nil
}
func (*dummySession) OpenRead(context.Context, provider.Object) (provider.Reader, error) {
	return nil, provider.ErrUnsupported
}
func (*dummySession) Close() error { return nil }
