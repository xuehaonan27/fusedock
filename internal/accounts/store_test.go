package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

type memorySecrets map[string]string

type countingSecrets struct {
	memorySecrets
	sets int
}

func (s *countingSecrets) Set(service, user, secret string) error {
	s.sets++
	return s.memorySecrets.Set(service, user, secret)
}

func (m memorySecrets) Get(service, user string) (string, error) {
	value, ok := m[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (m memorySecrets) Set(service, user, secret string) error {
	m[service+"/"+user] = secret
	return nil
}
func (m memorySecrets) Delete(service, user string) error {
	key := service + "/" + user
	if _, ok := m[key]; !ok {
		return keyring.ErrNotFound
	}
	delete(m, key)
	return nil
}

func newTestStore(t *testing.T) (*Store, memorySecrets) {
	t.Helper()
	secrets := memorySecrets{}
	return &Store{Path: filepath.Join(t.TempDir(), "fusedock", "accounts.json"), Secrets: secrets}, secrets
}

func TestAddResolveAndDefault(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	first := Account{Name: "pku", Provider: "pku", Username: "student", Options: map[string]string{"endpoint": "https://disk.example"}}
	if err := store.Add(ctx, first, map[string][]byte{"password": []byte("password")}, false, false); err != nil {
		t.Fatal(err)
	}
	second := Account{Name: "work", Provider: "pku", Username: "worker"}
	if err := store.Add(ctx, second, map[string][]byte{"password": []byte("password2")}, false, false); err != nil {
		t.Fatal(err)
	}
	config, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if config.Default != "pku" || len(config.Accounts) != 2 {
		t.Fatalf("unexpected config: %+v", config)
	}
	account, secrets, state, err := store.Credentials(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	if account.Provider != "pku" || string(secrets["password"]) != "password2" || len(state) != 0 {
		t.Fatalf("unexpected credentials: %+v %q %q", account, secrets, state)
	}
	if err := store.SetDefault(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	config, err = store.Load(ctx)
	if err != nil || config.Default != "work" {
		t.Fatalf("default = %q, err=%v", config.Default, err)
	}
	if err := store.Remove(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.Credentials(ctx, "work"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed account still resolves: %v", err)
	}
}

func TestSaveStateIsBoundToAccount(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	account := Account{Name: "pku", Provider: "pku", Username: "student"}
	if err := store.Add(ctx, account, map[string][]byte{"password": []byte("password")}, false, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(ctx, "pku", "pku", "student", []byte(`{"token":"x"}`)); err != nil {
		t.Fatal(err)
	}
	_, _, state, err := store.Credentials(ctx, "pku")
	if err != nil || string(state) != `{"token":"x"}` {
		t.Fatalf("state=%q err=%v", state, err)
	}
	if err := store.SaveState(ctx, "pku", "pku", "other", []byte(`{}`)); err == nil {
		t.Fatal("state was saved for a mismatched username")
	}
}

func TestSaveStateSkipsUnchangedCredentialWrite(t *testing.T) {
	ctx := context.Background()
	secrets := &countingSecrets{memorySecrets: memorySecrets{}}
	store := &Store{Path: filepath.Join(t.TempDir(), "fusedock", "accounts.json"), Secrets: secrets}
	account := Account{Name: "pku", Provider: "pku", Username: "student"}
	if err := store.Add(ctx, account, map[string][]byte{"password": []byte("password")}, false, false); err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"token":"stable"}`)
	if err := store.SaveState(ctx, "pku", "pku", "student", state); err != nil {
		t.Fatal(err)
	}
	writes := secrets.sets
	if err := store.SaveState(ctx, "pku", "pku", "student", state); err != nil {
		t.Fatal(err)
	}
	if secrets.sets != writes {
		t.Fatalf("unchanged state wrote the credential store %d extra times", secrets.sets-writes)
	}
}

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
