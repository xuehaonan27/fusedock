package namespace_test

import (
	"context"
	"errors"
	"github.com/xuehaonan27/fusedock/internal/namespace"
	"github.com/xuehaonan27/fusedock/internal/providers/local"
	"github.com/xuehaonan27/fusedock/pkg/provider"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvedScopeRejectsOutsideIDsAndRevokesReads(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "scope"), 0700)
	os.WriteFile(filepath.Join(root, "scope", "inside"), []byte("inside"), 0600)
	os.WriteFile(filepath.Join(root, "outside"), []byte("outside"), 0600)
	ctx := context.Background()
	s, err := local.Factory{}.Open(ctx, provider.OpenRequest{Options: map[string]string{"root": root}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, _ := namespace.NewAccountID()
	ns := namespace.Namespace{AccountID: id, ProviderID: "local"}
	target, err := namespace.Resolve(ctx, s, ns, "/scope/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.OpenRead(ctx, provider.Object{ID: "outside", Kind: provider.KindFile}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("outside object: %v", err)
	}
	o, err := provider.Resolve(ctx, target, "inside")
	if err != nil {
		t.Fatal(err)
	}
	r, err := target.OpenRead(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	target.Revoke()
	if _, err := r.ReadAt(ctx, make([]byte, 1), 0); !errors.Is(err, provider.ErrPermission) {
		t.Fatalf("revoked read: %v", err)
	}
	other, err := namespace.Resolve(ctx, s, ns, "scope")
	if err != nil {
		t.Fatal(err)
	}
	if other.VolumeID() != target.VolumeID() {
		t.Fatal("equivalent scope changed volume ID")
	}
}
func TestNamespaceEncodingAndAllocation(t *testing.T) {
	a, _ := namespace.NewAccountID()
	b, _ := namespace.NewAccountID()
	nodes := &namespace.Nodes{}
	left := namespace.NodeKey{Namespace: namespace.Namespace{AccountID: a, ProviderID: "pku"}, ObjectID: "same"}
	right := left
	right.Namespace.AccountID = b
	if nodes.ID(left) == nodes.ID(right) {
		t.Fatal("accounts aliased")
	}
	if nodes.ID(left) != nodes.ID(left) {
		t.Fatal("identity changed")
	}
	if string(namespace.Encode("ab", "c")) == string(namespace.Encode("a", "bc")) {
		t.Fatal("ambiguous key framing")
	}
}

type delayedStat struct {
	provider.Session
	started chan struct{}
	finish  chan struct{}
}

func (s *delayedStat) Root(context.Context) (provider.Object, error) {
	return provider.Object{ID: "root", Kind: provider.KindDirectory}, nil
}
func (s *delayedStat) Stat(_ context.Context, o provider.Object) (provider.Object, error) {
	close(s.started)
	<-s.finish
	return o, nil
}
func TestRevocationDuringMetadataRequest(t *testing.T) {
	s := &delayedStat{started: make(chan struct{}), finish: make(chan struct{})}
	id, _ := namespace.NewAccountID()
	target, err := namespace.Resolve(context.Background(), s, namespace.Namespace{AccountID: id, ProviderID: "fixture"}, "/")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := target.Stat(context.Background(), provider.Object{ID: "root"}); done <- err }()
	<-s.started
	target.Revoke()
	close(s.finish)
	if err := <-done; !errors.Is(err, provider.ErrPermission) {
		t.Fatalf("revoked metadata was delivered: %v", err)
	}
}
