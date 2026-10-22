// Package accounts stores account metadata on disk and secrets in the host's
// system credential store. Provider session state stays opaque to this package.
package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"
)

const (
	configVersion = 1
	secretService = "dev.fusedock.accounts"
	maxConfigSize = 1 << 20
)

var (
	ErrNotFound = errors.New("saved account not found")
	ErrExists   = errors.New("saved account already exists")
)

type Account struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Username string            `json:"username"`
	Options  map[string]string `json:"options,omitempty"`
	Created  time.Time         `json:"created"`
}

type Config struct {
	Version  int       `json:"version"`
	Default  string    `json:"default,omitempty"`
	Accounts []Account `json:"accounts"`
}

// Secrets is the subset of a system credential store used by accounts. Tests
// provide an in-memory implementation; production uses the login keychain,
// Secret Service, or Windows Credential Manager.
type Secrets interface {
	Get(service, user string) (string, error)
	Set(service, user, secret string) error
	Delete(service, user string) error
}

type Store struct {
	Path    string
	Secrets Secrets
}

type secretPayload struct {
	Provider string            `json:"provider"`
	Username string            `json:"username"`
	Secrets  map[string]string `json:"secrets,omitempty"`
	// Password is a legacy field accepted from the first account-store build.
	Password string          `json:"password,omitempty"`
	State    json.RawMessage `json:"state,omitempty"`
}

// Credential is a secret payload without account-store metadata. It is used by
// native hosts that receive the provider/account identity out of band.
type Credential struct {
	Provider string
	Username string
	Secrets  map[string][]byte
	State    []byte
}

// ReadCredential reads a system credential by local account name. Callers must
// verify Provider and Username against trusted metadata before using it.
func ReadCredential(name string) (Credential, error) {
	store := &Store{Secrets: newSystemSecrets()}
	secret, err := store.readSecret(name)
	if err != nil {
		return Credential{}, err
	}
	secrets := make(map[string][]byte, len(secret.Secrets)+1)
	for key, value := range secret.Secrets {
		secrets[key] = []byte(value)
	}
	if len(secrets) == 0 && secret.Password != "" {
		secrets["password"] = []byte(secret.Password)
	}
	return Credential{
		Provider: secret.Provider,
		Username: secret.Username,
		Secrets:  secrets,
		State:    append([]byte(nil), secret.State...),
	}, nil
}

// SaveCredentialState updates provider state after checking the provider and
// username stored inside the same credential item.
func SaveCredentialState(name, provider, username string, state []byte) error {
	store := &Store{Secrets: newSystemSecrets()}
	secret, err := store.readSecret(name)
	if err != nil {
		return err
	}
	if secret.Provider != provider || secret.Username != username {
		return errors.New("credential changed while provider state was being saved")
	}
	secret.State = append(json.RawMessage(nil), state...)
	return store.writeSecret(name, secret)
}

func Default() (*Store, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	return New(filepath.Join(dir, "fusedock", "accounts.json")), nil
}

func New(path string) *Store {
	return &Store{Path: path, Secrets: newSystemSecrets()}
}

func (s *Store) ensure() error {
	if s.Path == "" {
		return errors.New("account store path is required")
	}
	if s.Secrets == nil {
		s.Secrets = newSystemSecrets()
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create account directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("protect account directory: %w", err)
	}
	return nil
}

func (s *Store) withLock(ctx context.Context, exclusive bool, fn func() error) error {
	if err := s.ensure(); err != nil {
		return err
	}
	lock := flock.New(s.Path + ".lock")
	var locked bool
	var err error
	if exclusive {
		locked, err = lock.TryLockContext(ctx, 25*time.Millisecond)
	} else {
		locked, err = lock.TryRLockContext(ctx, 25*time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("lock account store: %w", err)
	}
	if !locked {
		return errors.New("lock account store: context ended")
	}
	defer lock.Close()
	return fn()
}

func (s *Store) Load(ctx context.Context) (Config, error) {
	var result Config
	err := s.withLock(ctx, false, func() error {
		var err error
		result, err = s.read()
		return err
	})
	return result, err
}

func (s *Store) read() (Config, error) {
	empty := Config{Version: configVersion, Accounts: []Account{}}
	file, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, fmt.Errorf("open account store: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return empty, fmt.Errorf("inspect account store: %w", err)
	}
	if !info.Mode().IsRegular() {
		return empty, errors.New("account store must be a regular file")
	}
	if err := checkConfigPermissions(info); err != nil {
		return empty, err
	}
	if info.Size() > maxConfigSize {
		return empty, errors.New("account store exceeds 1 MiB")
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return empty, fmt.Errorf("read account store: %w", err)
	}
	defer clear(data)
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return empty, errors.New("account store contains invalid JSON")
	}
	if err := validateConfig(config); err != nil {
		return empty, err
	}
	return config, nil
}

func validateConfig(config Config) error {
	if config.Version != configVersion {
		return errors.New("unsupported account store version")
	}
	names := make(map[string]bool, len(config.Accounts))
	for _, account := range config.Accounts {
		if !ValidName(account.Name) || names[account.Name] {
			return errors.New("account store contains an invalid or duplicate name")
		}
		if !ValidID(account.Provider) || strings.TrimSpace(account.Username) == "" {
			return fmt.Errorf("account %q has invalid provider metadata", account.Name)
		}
		for key := range account.Options {
			if !ValidKey(key) {
				return fmt.Errorf("account %q has invalid option %q", account.Name, key)
			}
		}
		names[account.Name] = true
	}
	if (config.Default == "" && len(config.Accounts) > 0) || (config.Default != "" && !names[config.Default]) {
		return errors.New("account store has an invalid default account")
	}
	return nil
}

func (s *Store) write(config Config) error {
	if err := validateConfig(config); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	defer clear(data)
	if len(data) > maxConfigSize {
		return errors.New("account store exceeds 1 MiB")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".accounts-*")
	if err != nil {
		return fmt.Errorf("create account store: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		return fmt.Errorf("install account store: %w", err)
	}
	return nil
}

func ValidName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func ValidID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func ValidKey(key string) bool { return ValidID(key) }

func (c Config) Find(name string) (Account, bool) {
	for _, account := range c.Accounts {
		if account.Name == name {
			return account, true
		}
	}
	return Account{}, false
}

func (s *Store) Add(ctx context.Context, account Account, secretValues map[string][]byte, makeDefault, replace bool) error {
	if !ValidName(account.Name) || !ValidID(account.Provider) || strings.TrimSpace(account.Username) == "" {
		return errors.New("invalid account metadata")
	}
	if len(secretValues) == 0 {
		return errors.New("account must have at least one secret")
	}
	for key, value := range secretValues {
		if !ValidKey(key) || len(value) == 0 || len(value) > 8192 {
			return fmt.Errorf("invalid account secret %q", key)
		}
	}
	return s.withLock(ctx, true, func() error {
		config, err := s.read()
		if err != nil {
			return err
		}
		existing, exists := config.Find(account.Name)
		if exists && !replace {
			return ErrExists
		}
		if exists && existing.Provider != account.Provider {
			return errors.New("account name already belongs to another provider")
		}
		secret, err := s.readSecret(account.Name)
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return err
		}
		previousSecret, previousExists := secret, err == nil
		secret.Provider = account.Provider
		secret.Username = account.Username
		secret.Secrets = make(map[string]string, len(secretValues))
		for key, value := range secretValues {
			secret.Secrets[key] = string(value)
		}
		secret.Password = ""
		if exists {
			secret.State = nil
		}
		if err := s.writeSecret(account.Name, secret); err != nil {
			return err
		}
		account.Options = cloneOptions(account.Options)
		if account.Created.IsZero() {
			if exists {
				account.Created = existing.Created
			} else {
				account.Created = time.Now().UTC()
			}
		}
		if exists {
			for i := range config.Accounts {
				if config.Accounts[i].Name == account.Name {
					config.Accounts[i] = account
					break
				}
			}
		} else {
			config.Accounts = append(config.Accounts, account)
		}
		sort.Slice(config.Accounts, func(i, j int) bool { return config.Accounts[i].Name < config.Accounts[j].Name })
		if makeDefault || config.Default == "" {
			config.Default = account.Name
		}
		if err := s.write(config); err != nil {
			if previousExists {
				_ = s.writeSecret(account.Name, previousSecret)
			} else {
				_ = s.Secrets.Delete(secretService, account.Name)
			}
			return err
		}
		return nil
	})
}

func (s *Store) SetDefault(ctx context.Context, name string) error {
	return s.withLock(ctx, true, func() error {
		config, err := s.read()
		if err != nil {
			return err
		}
		if _, ok := config.Find(name); !ok {
			return ErrNotFound
		}
		config.Default = name
		return s.write(config)
	})
}

func (s *Store) Remove(ctx context.Context, name string) error {
	return s.withLock(ctx, true, func() error {
		config, err := s.read()
		if err != nil {
			return err
		}
		found := false
		accounts := config.Accounts[:0]
		for _, account := range config.Accounts {
			if account.Name == name {
				found = true
				continue
			}
			accounts = append(accounts, account)
		}
		if !found {
			return ErrNotFound
		}
		config.Accounts = accounts
		if config.Default == name {
			config.Default = ""
			if len(config.Accounts) > 0 {
				config.Default = config.Accounts[0].Name
			}
		}
		if err := s.write(config); err != nil {
			return err
		}
		return s.Secrets.Delete(secretService, name)
	})
}

// Credentials resolves the password and opaque provider state for an account.
func (s *Store) Credentials(ctx context.Context, name string) (Account, map[string][]byte, []byte, error) {
	var account Account
	var secrets map[string][]byte
	var state []byte
	err := s.withLock(ctx, false, func() error {
		config, err := s.read()
		if err != nil {
			return err
		}
		var ok bool
		account, ok = config.Find(name)
		if !ok {
			return ErrNotFound
		}
		secret, err := s.readSecret(name)
		if err != nil {
			return fmt.Errorf("read account secret: %w", err)
		}
		if secret.Provider != account.Provider || secret.Username != account.Username {
			return errors.New("account metadata does not match the stored secret")
		}
		secrets = make(map[string][]byte, len(secret.Secrets)+1)
		for key, value := range secret.Secrets {
			secrets[key] = []byte(value)
		}
		if len(secrets) == 0 && secret.Password != "" {
			secrets["password"] = []byte(secret.Password)
		}
		state = append([]byte(nil), secret.State...)
		return nil
	})
	return account, secrets, state, err
}

// SaveState is safe to call from provider refresh paths. The account name is
// bound into the state payload so concurrent replacement cannot target a new
// account accidentally.
func (s *Store) SaveState(ctx context.Context, name, provider, username string, state []byte) error {
	return s.withLock(ctx, true, func() error {
		config, err := s.read()
		if err != nil {
			return err
		}
		account, ok := config.Find(name)
		if !ok || account.Provider != provider || account.Username != username {
			return errors.New("account changed while provider state was being saved")
		}
		secret, err := s.readSecret(name)
		if err != nil {
			return err
		}
		secret.State = append(json.RawMessage(nil), state...)
		return s.writeSecret(name, secret)
	})
}

func (s *Store) readSecret(name string) (secretPayload, error) {
	value, err := s.Secrets.Get(secretService, name)
	if err != nil {
		return secretPayload{}, err
	}
	defer clearString(&value)
	var payload secretPayload
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return secretPayload{}, errors.New("account secret contains invalid JSON")
	}
	return payload, nil
}

func (s *Store) writeSecret(name string, payload secretPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	defer clear(data)
	if len(data) > 3500 {
		return errors.New("account secret exceeds the system credential-store limit")
	}
	return s.Secrets.Set(secretService, name, string(data))
}

func cloneOptions(options map[string]string) map[string]string {
	if len(options) == 0 {
		return nil
	}
	result := make(map[string]string, len(options))
	for key, value := range options {
		result[key] = value
	}
	return result
}

func clearString(value *string) {
	if value != nil {
		*value = strings.Repeat("\x00", len(*value))
	}
}
