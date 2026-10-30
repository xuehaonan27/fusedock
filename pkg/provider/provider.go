// Package provider defines the stable boundary between FuseDock and a cloud
// storage implementation. Providers own remote semantics; mount drivers only
// translate file-system operations.
package provider

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound     = errors.New("object not found")
	ErrNotDirectory = errors.New("object is not a directory")
	ErrPermission   = errors.New("permission denied")
	ErrConflict     = errors.New("remote object changed")
	ErrUnsupported  = errors.New("operation is not supported by provider")
	ErrInvalidName  = errors.New("invalid object name")
)

// Kind is the small object model shared by every provider. Provider-specific
// types remain behind the adapter.
type Kind uint8

const (
	KindFile Kind = iota + 1
	KindDirectory
)

// Object is a snapshot of one remote object. ID must be stable for the lifetime
// of a Session and unique within it. ContentVersion identifies immutable bytes
// within that object when the provider advertises a qualifying read path.
// MetadataVersion and Version track metadata freshness only.
type Object struct {
	ID              string    `json:"id"`
	ParentID        string    `json:"parent_id"`
	Name            string    `json:"name"`
	Kind            Kind      `json:"kind"`
	Size            int64     `json:"size"`
	Version         string    `json:"version"` // metadata freshness; never persistent-cache proof
	ContentVersion  string    `json:"content_version,omitempty"`
	MetadataVersion string    `json:"metadata_version,omitempty"`
	Digest          Digest    `json:"digest,omitempty"`
	NoCache         bool      `json:"no_cache,omitempty"`
	Modified        time.Time `json:"modified"`
	Created         time.Time `json:"created"`
}

func (o Object) IsDir() bool { return o.Kind == KindDirectory }

// Capabilities describe operations supported by a provider implementation.
// The initial driver is read-only; mutation flags reserve the contract without
// pretending that writes are implemented.
type Capabilities uint32

const (
	CapRead Capabilities = 1 << iota
	CapMutate
	CapSearch
	CapShare
)

// OptionSpec lets the CLI describe provider configuration without hard-coded
// provider flags.
type OptionSpec struct {
	Identity    bool // participates in the account binding
	Key         string
	Description string
	Default     string
	Required    bool
}

// CredentialSpec describes a credential key understood by OpenRequest.
type CredentialSpec struct {
	Identity    bool // non-secret remote/application identity only
	Key         string
	Description string
	Secret      bool
	Required    bool
}

type Descriptor struct {
	ContractVersion  uint32
	ID               string
	Name             string
	Description      string
	Capabilities     Capabilities
	ReadCapabilities ReadCapabilities // upper bound; sessions and objects may narrow it
	Options          []OptionSpec
	Credentials      []CredentialSpec
}

// ContractVersion identifies the provider interface expected by this release.
// A sidecar protocol can negotiate this value before exposing a factory.
const ContractVersion uint32 = 1

type PromptKind string

const (
	PromptPassword PromptKind = "password"
	PromptOTP      PromptKind = "otp"
	PromptSMS      PromptKind = "sms"
	PromptCaptcha  PromptKind = "captcha"
)

// Prompt is an interactive challenge owned by the selected provider. Image is
// only set for image challenges such as CAPTCHA.
type Prompt struct {
	Kind    PromptKind
	Message string
	Secret  bool
	Image   []byte
}

type PromptFunc func(context.Context, Prompt) (string, error)

type OpenRequest struct {
	Endpoint string
	Username string
	Secrets  map[string][]byte
	Options  map[string]string
	Prompt   PromptFunc
	// State is opaque provider session state previously returned through
	// SaveState. Providers must treat it as untrusted and bind it to their
	// configured endpoint and account before restoring credentials.
	State []byte
	// SaveState asks the account service to persist refreshed state. Providers
	// that do not support restore should leave State empty and ignore this.
	SaveState func(context.Context, []byte) error
}

// Factory creates one provider session. A registry owns factories; a Session
// owns an authenticated account and any private clients or caches.
type Factory interface {
	Descriptor() Descriptor
	Open(context.Context, OpenRequest) (Session, error)
}

// AuthorizeRequest carries the provider-specific configuration needed to start
// an interactive account authorization. OpenURL opens the provider's trusted
// verification page; Notify writes a human-readable instruction to the CLI.
type AuthorizeRequest struct {
	Options map[string]string
	Secrets map[string][]byte
	Prompt  PromptFunc
	Notify  func(string)
	OpenURL func(string) error
}

// Authorization is the result of an interactive flow. State is an opaque
// renewable session persisted separately from secrets.
type Authorization struct {
	Username string
	Secrets  map[string][]byte
	State    []byte
}

// Authorizer is implemented by providers whose account setup needs more than a
// static password or token. The CLI remains responsible for credential storage.
type Authorizer interface {
	Authorize(context.Context, AuthorizeRequest) (Authorization, error)
}

// Reader is a random-access read handle. Each call carries the caller's context;
// a handle must not retain the context used to open it. Concurrent ReadAt calls
// are allowed. Providers must bound the work performed for each requested range.
type Reader interface {
	ReadAt(context.Context, []byte, int64) (int, error)
	io.Closer
}

// Session exposes a read-only hierarchical namespace. The initial public
// contract intentionally delays mutation and sharing semantics until adapters
// can express revision-based conflict handling consistently across providers.
type Session interface {
	Root(context.Context) (Object, error)
	Stat(context.Context, Object) (Object, error)
	List(context.Context, Object) ([]Object, error)
	OpenRead(context.Context, Object) (Reader, error)
	Close() error
}
