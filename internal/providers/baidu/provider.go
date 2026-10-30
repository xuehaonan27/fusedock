// Package baidu implements a read-only Baidu NetDisk provider using the official
// XPAN and OAuth APIs.
package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

const (
	rootID       = "baidu://root"
	defaultAPI   = "https://pan.baidu.com"
	defaultOAuth = "https://openapi.baidu.com"
	defaultScope = "basic,netdisk"
	stateVersion = 1
)

type Factory struct{}

func New() Factory { return Factory{} }

func (Factory) Descriptor() provider.Descriptor {
	return provider.Descriptor{
		ContractVersion:  provider.ContractVersion,
		ID:               "baidu",
		Name:             "Baidu NetDisk",
		Description:      "Read-only access to Baidu NetDisk through the official OAuth and XPAN APIs.",
		Capabilities:     provider.CapRead,
		ReadCapabilities: provider.ReadCapabilities{StableObjectID: true},
		Options: []provider.OptionSpec{
			{Key: "api_endpoint", Identity: true, Description: "XPAN API endpoint", Default: defaultAPI},
			{Key: "oauth_endpoint", Identity: true, Description: "Baidu OAuth endpoint", Default: defaultOAuth},
			{Key: "scope", Description: "OAuth scope", Default: defaultScope},
		},
		Credentials: []provider.CredentialSpec{
			{Key: "app_key", Identity: true, Description: "Baidu application API key", Required: true},
			{Key: "secret_key", Description: "Baidu application secret key", Secret: true, Required: true},
			{Key: "access_token", Description: "Optional saved access token", Secret: true},
			{Key: "refresh_token", Description: "Optional saved refresh token", Secret: true},
		},
	}
}

func (Factory) Open(ctx context.Context, req provider.OpenRequest) (provider.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	appKey := strings.TrimSpace(string(req.Secrets["app_key"]))
	secretKey := string(req.Secrets["secret_key"])
	if appKey == "" || secretKey == "" {
		return nil, errors.New("Baidu app_key and secret_key are required")
	}
	apiEndpoint := optionOrDefault(req.Options, "api_endpoint", defaultAPI)
	oauthEndpoint := optionOrDefault(req.Options, "oauth_endpoint", defaultOAuth)
	client, err := newClient(apiEndpoint, oauthEndpoint, appKey, secretKey, req.SaveState)
	if err != nil {
		return nil, err
	}
	username := strings.TrimSpace(req.Username)
	if len(req.State) > 0 {
		var state savedState
		if err := json.Unmarshal(req.State, &state); err != nil || state.Version != stateVersion {
			return nil, errors.New("saved Baidu session is invalid")
		}
		if state.APIEndpoint != apiEndpoint || state.OAuthEndpoint != oauthEndpoint || state.AppKey != appKey {
			return nil, errors.New("saved Baidu session belongs to a different application or endpoint")
		}
		if username != "" && state.Username != username {
			return nil, errors.New("saved Baidu session belongs to a different account")
		}
		username = state.Username
		client.restore(state)
	} else {
		client.accessToken = string(req.Secrets["access_token"])
		client.refreshToken = string(req.Secrets["refresh_token"])
	}
	if client.accessToken == "" && client.refreshToken == "" {
		return nil, errors.New("Baidu access token is missing; run 'fusedock account add --provider baidu'")
	}
	if client.accessToken == "" || client.expired() {
		if client.refreshToken == "" {
			return nil, errors.New("Baidu access token expired and no refresh token is available")
		}
		if err := client.refresh(ctx); err != nil {
			return nil, err
		}
	}
	if username == "" {
		username, err = client.userIdentity(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve Baidu account identity: %w", err)
		}
	}
	if err := client.setUsername(ctx, username); err != nil {
		return nil, err
	}
	return &Session{
		client: client,
		root:   meta{ID: rootID, Name: "Baidu NetDisk", Path: "/", Dir: true, ParentID: ""},
		known:  make(map[string]meta),
	}, nil
}

type Session struct {
	client *client
	root   meta
	mu     sync.Mutex
	known  map[string]meta
}

func (s *Session) Root(ctx context.Context) (provider.Object, error) {
	if err := ctx.Err(); err != nil {
		return provider.Object{}, err
	}
	return s.root.object(), nil
}

func (s *Session) Stat(ctx context.Context, object provider.Object) (provider.Object, error) {
	if object.ID == rootID || object.ID == "" {
		return s.Root(ctx)
	}
	s.mu.Lock()
	entry, ok := s.known[object.ID]
	s.mu.Unlock()
	if !ok {
		return provider.Object{}, provider.ErrNotFound
	}
	fresh, err := s.client.stat(ctx, object.ID)
	if err != nil {
		return provider.Object{}, err
	}
	fresh.ParentID = entry.ParentID
	s.mu.Lock()
	s.known[object.ID] = fresh
	s.mu.Unlock()
	return fresh.object(), nil
}

func (s *Session) List(ctx context.Context, parent provider.Object) ([]provider.Object, error) {
	if !parent.IsDir() {
		return nil, provider.ErrNotDirectory
	}
	path := "/"
	if parent.ID != rootID && parent.ID != "" {
		s.mu.Lock()
		entry, ok := s.known[parent.ID]
		s.mu.Unlock()
		if !ok || !entry.Dir {
			return nil, provider.ErrNotDirectory
		}
		path = entry.Path
	}
	entries, err := s.client.list(ctx, path)
	if err != nil {
		return nil, err
	}
	result := make([]provider.Object, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	s.mu.Lock()
	for _, entry := range entries {
		if !provider.ValidName(entry.Name) || entry.ID == "" {
			continue
		}
		if seen[entry.Name] {
			s.mu.Unlock()
			return nil, fmt.Errorf("Baidu returned duplicate child name %q", entry.Name)
		}
		seen[entry.Name] = true
		entry.ParentID = parent.ID
		s.known[entry.ID] = entry
		result = append(result, entry.object())
	}
	s.mu.Unlock()
	return result, nil
}

func (s *Session) OpenRead(ctx context.Context, object provider.Object) (provider.Reader, error) {
	if object.IsDir() || object.ID == rootID {
		return nil, provider.ErrNotDirectory
	}
	s.mu.Lock()
	entry, ok := s.known[object.ID]
	s.mu.Unlock()
	if !ok {
		return nil, provider.ErrNotFound
	}
	if entry.Size < 0 {
		return nil, errors.New("Baidu file size is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry.Size = object.Size
	entry.MD5 = object.Digest.Value
	entry.Modified = object.Modified
	return &reader{client: s.client, entry: entry}, nil
}

func (s *Session) Close() error {
	s.client.close()
	return nil
}

func optionOrDefault(options map[string]string, key, fallback string) string {
	if value := strings.TrimSpace(options[key]); value != "" {
		return strings.TrimRight(value, "/")
	}
	return fallback
}

// Canonical MD5 syntax alone does not certify mutation/concurrency semantics.
func (s *Session) ReadCapabilities(context.Context) (provider.ReadCapabilities, error) {
	return provider.ReadCapabilities{StableObjectID: true}, nil
}
