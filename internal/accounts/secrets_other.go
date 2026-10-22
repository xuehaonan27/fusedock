//go:build !darwin

package accounts

import "github.com/zalando/go-keyring"

type keyringSecrets struct{}

func newSystemSecrets() Secrets { return keyringSecrets{} }

func (keyringSecrets) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}
func (keyringSecrets) Set(service, user, secret string) error {
	return keyring.Set(service, user, secret)
}
func (keyringSecrets) Delete(service, user string) error {
	return keyring.Delete(service, user)
}
