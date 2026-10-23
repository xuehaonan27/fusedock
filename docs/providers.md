# Provider matrix

FuseDock providers implement `pkg/provider`. A provider should be added only when its observable file behavior is documented and its read path can be exercised without exposing user credentials in test output.

| Provider | Status | Read | Write | Main considerations |
| --- | --- | --- | --- | --- |
| Local directory | Implemented | Yes | No | Reference implementation; `os.Root` confines symlink traversal |
| PKU NetDisk | Implemented, read-only preview | Yes | No | IAAA login, AnyShare document libraries, signed range URLs, CAPTCHA/OTP prompts |
| Account broker | Implemented | Yes | No | Loopback capability, read-only object protocol, no credential exposure |
| Google Drive | Planned | No | No | OAuth with PKCE, shared drives, shortcuts, Google Docs export semantics |
| Baidu NetDisk | Planned | No | No | OAuth, rate limits, multipart range constraints, cloud-specific file types |
| S3-compatible | Planned | No | No | Endpoint configuration, list pagination, object ETags, multipart uploads |
| WebDAV | Planned | No | No | Server-specific property support, locks, weak metadata, redirects |

## PKU NetDisk

The PKU provider logs in through the university IAAA service and uses the AnyShare API exposed by the official client. The root lists accessible document libraries; descendants are ordinary directories and files.

Object IDs are full `gns://` document IDs. Versions use AnyShare revision values. A cold byte-range read obtains a revision-specific signed object URL, sends one checked `Range` request, and rejects a mismatched total size or range. Signed URLs never appear in returned errors.

The current provider is intentionally read-only. It does not use sharing, recycle-bin, upload, or fast-upload endpoints. Those can be added behind capability-gated interfaces rather than widening the read contract.

The checked-in fixtures cover IAAA page parsing, AnyShare library pagination shape, signed headers, and byte-range validation. A separate opt-in test checks the live public login page without using an account:

```sh
FUSEDOCK_LIVE_PKU=1 go test -run '^TestLivePKULoginPage$' -v ./internal/providers/pku
```

## Google Drive

Google Drive should be a separate read adapter first. The implementation must distinguish ordinary binary files, native Google Docs/Sheets/Slides, shortcuts, and files in shared drives. Native documents need an explicit export format; mounting them as editable source files would create surprising or lossy writes.

OAuth should use PKCE and the system browser. Tokens must be stored as secrets bound to the account and provider. A read adapter should expose stable file IDs and use Drive revisions or content checksums for cache identity.

## Baidu NetDisk

Baidu's API needs its own pagination, token refresh, rate-limit, and multipart-range behavior. Cloud-specific constructs such as offline downloads, share links, and archives should be surfaced as provider operations instead of being forced into POSIX names.

The read adapter should validate every returned path component, include stable file identity in cache keys, and keep signed URLs out of logs. Write support waits for the shared conflict and recovery contract.

## Generic protocols

WebDAV and S3-compatible services are useful low-risk providers for developing common behavior. Neither should be treated as a universal abstraction for Google Drive or Baidu: their authentication, object types, metadata, and mutation semantics still need provider-specific adapters.

## Adding a provider

1. Add a package under `internal/providers` initially.
2. Implement the read session contract in `pkg/provider`.
3. Add fixture tests plus `pkg/providertest` conformance checks.
4. Register the factory in `internal/providers/providers.go`.
5. Document platform-specific name limits, authentication, rate-limit behavior, and unsupported operations.
6. Keep credentials and signed URLs out of diagnostics.

A provider's status should remain explicit. A partially implemented adapter must not advertise write, search, or sharing capabilities it cannot honor.
