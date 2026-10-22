# Writing a provider

A provider adapts one remote service to FuseDock's small read namespace contract. The mount driver already handles inode construction, attribute translation, and read planning.

## Minimal implementation

Implement `provider.Factory` and `provider.Session`:

```go
import "github.com/xuehaonan/fusedock/pkg/provider"

type Factory struct{}

func (Factory) Descriptor() provider.Descriptor {
    return provider.Descriptor{
		ContractVersion: provider.ContractVersion,
        ID: "example",
        Name: "Example Drive",
        Capabilities: provider.CapRead,
        Options: []provider.OptionSpec{
            {Key: "endpoint", Description: "API endpoint", Required: true},
        },
        Credentials: []provider.CredentialSpec{
            {Key: "token", Description: "OAuth access token", Secret: true, Required: true},
        },
    }
}

func (Factory) Open(ctx context.Context, req provider.OpenRequest) (provider.Session, error) {
    // Authenticate, validate the endpoint, and return a session.
}

type Session struct{ /* clients and account identity */ }

func (*Session) Root(context.Context) (provider.Object, error) { /* ... */ }
func (*Session) Stat(context.Context, provider.Object) (provider.Object, error) { /* ... */ }
func (*Session) List(context.Context, provider.Object) ([]provider.Object, error) { /* ... */ }
func (*Session) OpenRead(context.Context, provider.Object) (provider.Reader, error) { /* ... */ }
func (*Session) Close() error { /* ... */ }
```

Register it in `internal/providers/providers.go`, then run:

```sh
go run ./cmd/fusedock providers
go run ./cmd/fusedock ls example --option endpoint=https://api.example
```

## Object rules

Choose the contract version declared by the SDK. `ID` is the primary identity. Use the provider's stable file ID, not a display path. `ParentID` is useful for diagnostics and dependency caches but must not replace the full ID.

`Version` drives cache correctness and future optimistic concurrency checks. Prefer an opaque revision, ETag, or content hash. When a provider lacks one, combine a stable modified timestamp with size and type; document the weaker guarantee.

`List` must return only direct children and close every provider-level page. It should sort deterministically for stable presentation, although the driver does not require order. Do not resolve a child by concatenating an unchecked display path into an API URL.

`Name` must pass `provider.ValidName`. If a remote object cannot be represented safely, omit it from the mounted namespace and expose its contents through a separate provider-aware command rather than allowing a path escape.

For duplicate library or root names, disambiguate deterministically inside the provider. The internal ID remains the object identity.

## Reads

`OpenRead` validates object type and returns a handle. `ReadAt` must accept the current caller's context; it must not use the context that created the handle.

For remote HTTP storage, return only bytes covered by a validated `Content-Range`. Reject a signed URL with a different origin or scheme than expected. Never include a signed URL, query string, token, or credential header in an error.

The VFS requests up to 4 MiB at a time. A provider may fetch exact ranges or a larger server-supported unit, but it must answer the requested slice without inventing data. Bound response bodies and validate revision size when the API exposes it.

## Authentication

Use `OpenRequest.Options` for non-secret configuration and `OpenRequest.Secrets` for credentials. The CLI clears the secret map after `Open` returns. If a session retains a credential, make an explicit copy and keep it in a private field; do not include it in formatted errors.

Use `Prompt` for interactive CAPTCHA, OTP, SMS, or password entry. The CLI provides a terminal implementation. A service deployment should supply credentials through its environment or a future private credential store.

OAuth providers should use PKCE and the system browser. They must validate redirect origins and must not run an unauthenticated loopback listener that any local process can use to inject an account.

## Capabilities and future mutations

The current session contract is read-only. Set `CapRead`. Do not claim `CapMutate` until the provider has a well-tested implementation of local staging, supported rename semantics, revision checks, and recovery behavior.

Cloud-native objects that do not map to files should be surfaced as provider-specific commands or application views. Pretending a Google Doc is an editable local Word file creates silent data loss.

## Testing

Use an HTTP fixture or provider mock with no real credentials. Cover at least:

- Root and nested listing, including pagination and unusual Unicode names.
- Stable IDs across a rename.
- Unknown and permission-denied errors.
- Partial reads, end-of-file behavior, and byte-range validation.
- Concurrent metadata refresh and read cancellation.
- Credential redaction in error text.
- Path traversal and duplicate-name rejection.

Run:

```sh
go test -race ./...
```

Pass an authenticated fixture session to the shared conformance runner:

```go
object := provider.Object{ /* a small regular fixture file */ }
providertest.Run(t, session, providertest.Options{
    MaxEntries: 100,
    ReadObject: &object,
    ReadOffset: 1,
    ReadLength: 4096,
})
```

The runner checks root discovery, one listing, stable non-empty IDs, valid names, parent identity, and a range read. Provider-specific tests still need pagination, authentication failures, and mutation semantics where applicable.

Live provider tests must be opt-in, use a dedicated account, avoid destructive calls unless explicitly authorized, and never print secrets or signed URLs.

## Provider checklist

- Descriptor ID is stable and lowercase.
- Options have useful descriptions and safe defaults.
- Every credential is marked secret where applicable.
- All remote names and IDs are validated.
- Context reaches every network call.
- Handle close releases connections and temporary files.
- Errors preserve enough detail to diagnose failures without exposing secrets.
- Read-only promises are real: no hidden upload, import, conversion, or write request.
