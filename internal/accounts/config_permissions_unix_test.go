//go:build !windows

package accounts

import (
	"context"
	"os"
	"testing"
)

func TestConfigFileIsOwnerOnly(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	account := Account{Name: "pku", Provider: "pku", Username: "student"}
	if err := store.Add(ctx, account, map[string][]byte{"password": []byte("password")}, false, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
	if err := os.Chmod(store.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); err == nil {
		t.Fatal("world-readable account store was accepted")
	}
}
