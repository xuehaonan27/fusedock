package accounts

import (
	"context"
	"testing"
)

func TestBindingRotationRenameRecreation(t *testing.T) {
	ctx := context.Background()
	store, secrets := newTestStore(t)
	a := Account{Name: "one", Provider: "pku", Username: "alice", BindingFields: map[string]string{"endpoint": "HTTPS://EXAMPLE.COM/"}}
	put := func(replace bool) {
		t.Helper()
		if err := store.Add(ctx, a, map[string][]byte{"password": []byte("secret")}, false, replace); err != nil {
			t.Fatal(err)
		}
	}
	load := func(name string) Account {
		t.Helper()
		a, _, _, err := store.Credentials(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	put(false)
	first := load("one")
	if !first.AccountID.Valid() || first.CredentialRef == first.Name {
		t.Fatal("identity is an alias")
	}
	a.BindingFields["endpoint"] = "https://example.com"
	put(true)
	rotated := load("one")
	if rotated.AccountID != first.AccountID {
		t.Fatal("normalized binding rotation changed identity")
	}
	if err := store.Rename(ctx, "one", "renamed"); err != nil {
		t.Fatal(err)
	}
	renamed := load("renamed")
	if renamed.AccountID != first.AccountID || renamed.CredentialRef != first.CredentialRef || len(secrets) != 1 {
		t.Fatal("rename moved credentials")
	}
	if err := store.SaveBoundState(ctx, first, []byte(`{"refresh":"v2"}`)); err != nil {
		t.Fatal(err)
	}
	a.Name = "renamed"
	a.BindingFields["endpoint"] = "https://other.example"
	put(true)
	rebound := load("renamed")
	if rebound.AccountID == first.AccountID {
		t.Fatal("rebind reused identity")
	}
	if err := store.SaveBoundState(ctx, first, []byte(`{}`)); err == nil {
		t.Fatal("old session updated rebound credential")
	}
	if err := store.Remove(ctx, "renamed"); err != nil {
		t.Fatal(err)
	}
	put(false)
	if load("renamed").AccountID == rebound.AccountID {
		t.Fatal("recreation reused identity")
	}
}
