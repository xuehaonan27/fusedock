// Package broker is the provider-side client used by the sandboxed FSKit
// extension. It exchanges a one-use bootstrap capability for an in-memory
// session capability and performs read-only object requests over loopback.
package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	brokerapi "github.com/xuehaonan27/fusedock/internal/broker"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type Factory struct{}

func New() Factory { return Factory{} }

func (Factory) Descriptor() provider.Descriptor {
	return provider.Descriptor{
		ContractVersion:  provider.ContractVersion,
		ID:               "broker",
		Name:             "FuseDock account broker",
		Description:      "Read-only loopback access to a broker holding a saved account session.",
		Capabilities:     provider.CapRead,
		ReadCapabilities: provider.ReadCapabilities{StableObjectID: true, ExactRange: true, DeclaredDigest: true},
		Options: []provider.OptionSpec{
			{Key: "endpoint", Description: "Loopback broker endpoint", Required: true},
		},
		Credentials: []provider.CredentialSpec{
			{Key: "token", Description: "One-use broker bootstrap capability", Secret: true, Required: true},
		},
	}
}

func (Factory) Open(ctx context.Context, req provider.OpenRequest) (provider.Session, error) {
	base, err := validateEndpoint(req.Options["endpoint"])
	if err != nil {
		return nil, err
	}
	bootstrap := string(req.Secrets["token"])
	if len(bootstrap) < 32 || len(bootstrap) > 256 {
		return nil, errors.New("broker bootstrap capability is invalid")
	}
	client := &client{base: base, transport: brokerTransport(base)}
	var claim brokerapi.ClaimResponse
	if err := client.post(ctx, "/v2/claim", bootstrap, struct{}{}, &claim); err != nil {
		return nil, fmt.Errorf("claim broker capability: %w", err)
	}
	if claim.Version != brokerapi.ProtocolVersion || len(claim.Token) < 32 {
		return nil, errors.New("broker returned an incompatible capability response")
	}
	client.setToken(claim.Token)
	return &Session{client: client, caps: claim.Capabilities}, nil
}

type client struct {
	base      *url.URL
	transport *http.Transport
	mu        sync.RWMutex
	token     string
}

func (c *client) setToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

func (c *client) dataToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

func (c *client) post(ctx context.Context, path, token string, input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base.String()+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.transport.RoundTrip(req)
	if err != nil {
		return errors.New("broker transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeResponseError(resp)
	}
	if output == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 32<<20))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode broker response: %w", err)
	}
	return nil
}

type Session struct {
	caps   provider.ReadCapabilities
	client *client
	mu     sync.Mutex
	closed bool
}

func (s *Session) Root(ctx context.Context) (provider.Object, error) {
	var object provider.Object
	if err := s.client.post(ctx, "/v2/root", s.client.dataToken(), struct{}{}, &object); err != nil {
		return provider.Object{}, err
	}
	return validateObject(object, "")
}

func (s *Session) List(ctx context.Context, parent provider.Object) ([]provider.Object, error) {
	if !parent.IsDir() {
		return nil, provider.ErrNotDirectory
	}
	var objects []provider.Object
	request := brokerapi.ListRequest{ParentID: parent.ID}
	if err := s.client.post(ctx, "/v2/list", s.client.dataToken(), request, &objects); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(objects))
	for i := range objects {
		object, err := validateObject(objects[i], parent.ID)
		if err != nil {
			return nil, err
		}
		if seen[object.Name] {
			return nil, fmt.Errorf("broker returned duplicate child name %q", object.Name)
		}
		seen[object.Name] = true
		objects[i] = object
	}
	return objects, nil
}

func (s *Session) Stat(ctx context.Context, object provider.Object) (provider.Object, error) {
	var result provider.Object
	request := brokerapi.StatRequest{ObjectID: object.ID}
	if err := s.client.post(ctx, "/v2/stat", s.client.dataToken(), request, &result); err != nil {
		return provider.Object{}, err
	}
	return validateObject(result, object.ParentID)
}

func (s *Session) OpenRead(ctx context.Context, object provider.Object) (provider.Reader, error) {
	if object.IsDir() {
		return nil, provider.ErrNotDirectory
	}
	if object.ID == "" || object.Size < 0 {
		return nil, errors.New("broker file metadata is incomplete")
	}
	var opened brokerapi.OpenResponse
	request := brokerapi.OpenRequest{ObjectID: object.ID}
	if s.caps.ExactRange && object.ContentVersion != "" {
		revision := object.Revision()
		request.Revision = &revision
	}
	if err := s.client.post(ctx, "/v2/open", s.client.dataToken(), request, &opened); err != nil {
		return nil, err
	}
	if opened.Handle == "" || opened.Snapshot.ID != object.ID {
		return nil, errors.New("invalid broker read handle")
	}
	return &reader{session: s, object: opened.Snapshot, handle: opened.Handle}, nil
}

func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.client.post(ctx, "/v2/close", s.client.dataToken(), struct{}{}, nil)
	s.client.transport.CloseIdleConnections()
	s.client.setToken("")
	return nil
}

type reader struct {
	handle  string
	session *Session
	object  provider.Object
	mu      sync.Mutex
	closed  bool
}

func (r *reader) ReadAt(ctx context.Context, dest []byte, offset int64) (int, error) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return 0, errors.New("broker read handle is closed")
	}
	if offset < 0 || offset > r.object.Size {
		return 0, errors.New("read offset is outside the broker object")
	}
	if len(dest) == 0 || offset == r.object.Size {
		return 0, nil
	}
	length := min(int64(len(dest)), r.object.Size-offset)
	request := brokerapi.ReadRequest{Handle: r.handle, Offset: offset, Length: length}
	data, err := json.Marshal(request)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.session.client.base.String()+"/v2/read", bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+r.session.client.dataToken())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := r.session.client.transport.RoundTrip(req)
	if err != nil {
		return 0, errors.New("broker read transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, decodeResponseError(resp)
	}
	n, err := io.ReadFull(resp.Body, dest[:length])
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return n, io.EOF
		}
		return n, err
	}
	var extra [1]byte
	if count, extraErr := resp.Body.Read(extra[:]); count != 0 || (extraErr != nil && !errors.Is(extraErr, io.EOF)) {
		return 0, errors.New("broker returned an oversized read response")
	}
	return n, nil
}

func (r *reader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.session.client.post(ctx, "/v2/release", r.session.client.dataToken(), brokerapi.ReleaseRequest{Handle: r.handle}, nil)
}

func validateEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("broker endpoint must be a loopback HTTP origin")
	}
	if u.Port() == "" {
		return nil, errors.New("broker endpoint must include a port")
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || !addr.IsLoopback() {
		return nil, errors.New("broker endpoint must use a literal loopback IP address")
	}
	return u, nil
}

func brokerTransport(endpoint *url.URL) *http.Transport {
	target := endpoint.Host
	return &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, target)
		},
	}
}

func validateObject(object provider.Object, expectedParent string) (provider.Object, error) {
	if object.ID == "" || len(object.ID) > 8192 || !provider.ValidName(object.Name) ||
		(object.Kind != provider.KindFile && object.Kind != provider.KindDirectory) || object.Size < 0 {
		return provider.Object{}, errors.New("broker returned invalid object metadata")
	}
	if expectedParent != "" && object.ParentID != expectedParent {
		return provider.Object{}, errors.New("broker returned an object with an unexpected parent")
	}
	return object, nil
}

func decodeResponseError(resp *http.Response) error {
	var response brokerapi.ErrorResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4096))
	_ = decoder.Decode(&response)
	switch response.Code {
	case "not_found":
		return provider.ErrNotFound
	case "permission":
		return provider.ErrPermission
	case "conflict":
		return provider.ErrConflict
	case "not_directory":
		return provider.ErrNotDirectory
	case "unsupported":
		return provider.ErrUnsupported
	case "cancelled":
		return context.Canceled
	case "timeout":
		return context.DeadlineExceeded
	case "auth", "claim":
		return provider.ErrPermission
	}
	return fmt.Errorf("broker request failed with HTTP %d", resp.StatusCode)
}

func (r *reader) Snapshot() provider.Object { return r.object }

func (s *Session) ReadCapabilities(context.Context) (provider.ReadCapabilities, error) {
	return s.caps, nil
}
func (s *Session) OpenReadRevision(ctx context.Context, r provider.ObjectRevision) (provider.Reader, error) {
	if !s.caps.ExactRange {
		return nil, provider.ErrUnsupported
	}
	return s.OpenRead(ctx, provider.Object{ID: r.ObjectID, Kind: provider.KindFile, Size: r.Size, ContentVersion: r.ContentVersion, Digest: r.Digest})
}
