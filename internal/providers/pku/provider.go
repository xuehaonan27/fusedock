package pku

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

const rootID = "pku://root"

type Factory struct{}

func New() Factory { return Factory{} }

func (Factory) Descriptor() provider.Descriptor {
	return provider.Descriptor{
		ContractVersion: provider.ContractVersion,
		ID:              "pku",
		Name:            "PKU NetDisk",
		Description:     "Read-only access to Peking University NetDisk through IAAA and AnyShare.",
		Capabilities:    provider.CapRead,
		Options: []provider.OptionSpec{
			{Key: "endpoint", Description: "AnyShare endpoint", Default: defaultEndpoint},
			{Key: "iaaa_endpoint", Description: "IAAA endpoint", Default: defaultIAAA},
		},
		Credentials: []provider.CredentialSpec{
			{Key: "username", Description: "PKU campus account", Required: true},
			{Key: "password", Description: "PKU account password", Secret: true, Required: true},
		},
	}
}

func (Factory) Open(ctx context.Context, req provider.OpenRequest) (provider.Session, error) {
	username := strings.TrimSpace(req.Username)
	if username == "" {
		username = strings.TrimSpace(string(req.Secrets["username"]))
	}
	if username == "" {
		return nil, errors.New("PKU username is required (--username or --credential username=VALUE)")
	}
	password := req.Secrets["password"]
	if len(password) == 0 && req.Prompt != nil {
		value, err := req.Prompt(ctx, provider.Prompt{Kind: provider.PromptPassword, Message: "PKU password", Secret: true})
		if err != nil {
			return nil, err
		}
		password = []byte(value)
	}
	if len(password) == 0 {
		return nil, errors.New("PKU password is required; use --credential-env password=ENV or run interactively")
	}
	defer clear(password)

	endpoint := strings.TrimSpace(req.Options["endpoint"])
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	auth, err := newAuthSession(endpoint, req.Options["iaaa_endpoint"])
	if err != nil {
		return nil, err
	}
	if err := auth.Login(ctx, username, password, req.Prompt); err != nil {
		auth.Close()
		return nil, err
	}
	client, err := newAnyShareClient(endpoint, auth)
	if err != nil {
		auth.Close()
		return nil, err
	}
	return &Session{auth: auth, client: client, endpoint: endpoint}, nil
}

type Session struct {
	auth     *authSession
	client   *anyShareClient
	endpoint string
}

func (s *Session) Root(ctx context.Context) (provider.Object, error) {
	if err := ctx.Err(); err != nil {
		return provider.Object{}, err
	}
	return provider.Object{ID: rootID, Name: "PKU NetDisk", Kind: provider.KindDirectory}, nil
}

func (s *Session) Stat(ctx context.Context, object provider.Object) (provider.Object, error) {
	if object.ID == rootID {
		return s.Root(ctx)
	}
	if object.ID == "" {
		return provider.Object{}, provider.ErrNotFound
	}
	entry, err := s.client.stat(ctx, toEntry(object))
	if err != nil {
		return provider.Object{}, err
	}
	return fromEntry(entry, object.ParentID), nil
}

func (s *Session) List(ctx context.Context, parent provider.Object) ([]provider.Object, error) {
	if !parent.IsDir() {
		return nil, provider.ErrNotDirectory
	}
	var entries []pkuEntry
	var err error
	if parent.ID == rootID {
		entries, err = s.client.roots(ctx, false)
	} else {
		entries, err = s.client.list(ctx, parent.ID)
	}
	if err != nil {
		return nil, err
	}
	result := make([]provider.Object, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.ID == "" || !provider.ValidName(entry.Name) {
			return nil, errors.New("PKU returned an object with an invalid ID or name")
		}
		if seen[entry.Name] {
			return nil, fmt.Errorf("PKU returned duplicate child name %q", entry.Name)
		}
		seen[entry.Name] = true
		result = append(result, fromEntry(entry, parent.ID))
	}
	return result, nil
}

func (s *Session) OpenRead(ctx context.Context, object provider.Object) (provider.Reader, error) {
	if object.IsDir() || object.ID == rootID {
		return nil, provider.ErrNotDirectory
	}
	if object.ID == "" || object.Size < 0 {
		return nil, errors.New("PKU file metadata is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &reader{client: s.client, entry: toEntry(object)}, nil
}

func (s *Session) Close() error {
	if s.auth != nil {
		s.auth.Close()
	}
	if s.client != nil {
		s.client.client.CloseIdleConnections()
		s.client.objects.CloseIdleConnections()
	}
	return nil
}

type reader struct {
	client *anyShareClient
	entry  pkuEntry
}

func (r *reader) ReadAt(ctx context.Context, dest []byte, off int64) (int, error) {
	return r.client.readAt(ctx, r.entry, dest, off)
}

func (r *reader) Close() error { return nil }

func fromEntry(entry pkuEntry, parent string) provider.Object {
	kind, size := provider.KindFile, entry.Size
	if entry.Size < 0 || strings.HasSuffix(entry.Type, "doc_lib") || entry.Type == "folder" || entry.Type == "dir" {
		kind, size = provider.KindDirectory, 0
	}
	return provider.Object{
		ID: firstNonEmpty(entry.ID, entry.DocID), ParentID: parent, Name: entry.Name,
		Kind: kind, Size: size, Version: entry.Rev, Modified: entry.Modified, Created: entry.Created,
	}
}

func toEntry(object provider.Object) pkuEntry {
	size := object.Size
	if object.IsDir() {
		size = -1
	}
	return pkuEntry{ID: object.ID, DocID: object.ID, Name: object.Name, Size: size, Rev: object.Version, Modified: object.Modified, Created: object.Created}
}
