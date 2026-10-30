package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"

	"github.com/xuehaonan27/fusedock/internal/namespace"
)

// BindingFields contains provider-declared identity fields, already populated
// with provider defaults. Operational settings and credentials are excluded.
func (a Account) BindingFingerprint() string {
	fields := []string{"fusedock-binding-v1", a.Provider, strings.TrimSpace(a.Username)}
	keys := make([]string, 0, len(a.BindingFields))
	for k := range a.BindingFields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := strings.TrimSpace(a.BindingFields[k])
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			u.Scheme = strings.ToLower(u.Scheme)
			u.Host = strings.ToLower(u.Host)
			u.Path = strings.TrimRight(u.Path, "/")
			v = u.String()
		}
		fields = append(fields, k, v)
	}
	h := sha256.Sum256(namespace.Encode(fields...))
	return "v1:" + hex.EncodeToString(h[:])
}
func (a Account) credentialKey() string {
	if a.CredentialRef != "" {
		return a.CredentialRef
	}
	return a.Name
}
func (a Account) CredentialKey() string { return a.credentialKey() }
func (s *Store) Rename(ctx context.Context, oldName, newName string) error {
	if !ValidName(newName) {
		return errors.New("invalid account alias")
	}
	return s.withLock(ctx, true, func() error {
		c, err := s.read()
		if err != nil {
			return err
		}
		for _, a := range c.Accounts {
			if strings.EqualFold(a.Name, newName) {
				return ErrExists
			}
		}
		for i := range c.Accounts {
			a := &c.Accounts[i]
			if a.Name != oldName {
				continue
			}
			if a.AccountID == "" {
				return errors.New("this account predates stable identities")
			}
			a.Name = newName
			if c.Default == oldName {
				c.Default = newName
			}
			return s.write(c)
		}
		return ErrNotFound
	})
}

// SaveBoundState matches the immutable binding, so refresh from a removed or
// replaced session cannot overwrite the new account's credentials.
func (s *Store) SaveBoundState(ctx context.Context, opened Account, state []byte) error {
	return s.saveState(ctx, opened.Name, opened.Provider, opened.Username, opened.AccountID, opened.Binding, state)
}
