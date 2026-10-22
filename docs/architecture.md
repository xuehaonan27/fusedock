# FuseDock architecture

FuseDock separates remote storage semantics from the local file-system ABI. The boundary is deliberately small enough that a new provider does not need to know about FUSE inodes, WinFsp handles, or GUI request shapes.

## Language choice

The first implementation uses Go. The `go-fuse` and WinFsp/cgofuse ecosystems already cover both required desktop filesystem drivers, Go cross-compiles the core and CLI without CGO on Linux, macOS, and Windows, and its HTTP, streaming, and concurrency facilities are sufficient for provider adapters. This keeps the first read path testable without introducing a native build for every platform.

Rust remains viable for a future provider sidecar or a replacement driver when resource control or native packaging justifies it. The versioned provider contract is the boundary; the choice of implementation language is not baked into object IDs, cache keys, or CLI semantics.

## Layers

| Layer | Responsibility |
| --- | --- |
| Provider | Authentication, namespace traversal, metadata, stable object IDs, byte-range reads |
| VFS | Request coalescing, metadata TTLs, read planning, and later write-back/recovery |
| Driver | Translation to go-fuse, WinFsp, or another local filesystem API |
| Surface | CLI, desktop GUI, tray, or mobile host |

The current packages are:

| Package | Responsibility |
| --- | --- |
| `pkg/provider` | Public provider contract, object model, registry, path resolver, common errors |
| `internal/providers/local` | Local directory reference implementation confined by `os.Root` |
| `internal/providers/pku` | IAAA authentication and AnyShare namespace/range adapter |
| `internal/vfs` | Metadata cache and per-handle block read cache |
| `internal/fusefs` | Read-only Linux FUSE adapter; explicit FSKit guidance on macOS |
| `internal/credentials` | Owner-only credential/configuration file parser |
| `internal/accounts` | Account metadata, OS credential storage, default selection, and session-state persistence |
| `macos/FSKit` | Swift FSKit app extension and Xcode project |
| `macos/bridge` | C ABI and Go `c-archive` for the FSKit extension |
| `internal/cli` | Provider discovery, inspection, and mount orchestration |

## Provider invariants

A `provider.Session` represents one authenticated account and one namespace.

1. `Root` returns a directory object and does not perform a vendor-specific path lookup.
2. Every object ID is non-empty, stable for the session, and unique within that session.
3. `Version` changes whenever file content or relevant metadata changes. Providers that do not have a native revision should synthesize one from a stable timestamp and size.
4. Every child's `Name` is a single path component. `/`, `\`, NUL, `.` and `..` are invalid. Duplicate names in one listing are an error.
5. `List` returns a complete snapshot for one directory. Provider pagination stays inside the adapter.
6. `OpenRead` returns a handle whose `ReadAt` accepts the caller's context on every call. It must not retain the open call's context.
7. `ReadAt` either returns requested bytes or an error. It must not silently return zeros for missing remote ranges.
8. A session is safe for concurrent `Stat`, `List`, and `OpenRead` calls unless its documentation explicitly states otherwise.

Names are presentation values. The driver may cache or revalidate attributes, but identity, reads, and future mutations use `Object.ID`. This prevents a renamed or shared vendor object from accidentally resolving to a different file.

Common errors (`ErrNotFound`, `ErrPermission`, `ErrConflict`, and others) are translated once by the driver. Vendor HTTP codes and signed URLs never become FUSE errno values directly.

## Read path

The metadata cache stores short-lived directory snapshots and attributes. Concurrent requests for the same directory share one provider request, and the waiting caller can cancel without cancelling a useful request for another caller.

An open file has one read handle. The VFS layer turns driver-size reads into 4 MiB aligned provider ranges and retains the most recently loaded block per handle. This removes repeated authentication and signed-URL work for ordinary sequential reads. It is an in-memory per-handle cache, not a durable disk cache.

PKU performs two requests for a cold range: `osdownload` obtains a signed object request, and the object storage returns that exact byte range. The adapter verifies `Content-Range` and the selected revision size before exposing bytes. Signed URLs and their query strings are omitted from errors.

The kernel can issue reads after the handle's original open request has returned. Passing context per `ReadAt` is therefore part of the provider contract.

## Account and cache identity

The eventual cache key must include:

```text
account namespace + provider ID + object ID + version + byte range
```

The current release mounts one session per mount point, so the account dimension is implicit. Multi-account mounts will add a virtual first-level account directory and scope IDs before they reach a provider. A shared library must not be confused with a local copy merely because two accounts can see the same name.

The read cache is not a synchronization cache. A changed version causes a new range; it does not merge local and remote contents.

## Writes and conflict handling

The read contract is intentionally separate from mutation capabilities. A future write contract will add operations such as create, write, truncate, rename, and remove behind explicit provider capabilities.

Writes should follow this sequence:

1. Build a private local staging file and record the expected remote version.
2. Return successful close or `fsync` only after the provider publishes the write.
3. Send the expected version with the mutation. A version mismatch returns `ErrConflict`, which maps to `EBUSY`.
4. Retain failed staged data and a recovery journal instead of acknowledging a durable write.
5. Never retry a non-idempotent mutation automatically without a provider-specific idempotency key.

Cross-account or cross-provider rename is `EXDEV`; copying followed by deletion remains an explicit operation. Provider-specific features such as Google Docs or Baidu offline tasks do not map to POSIX files and should stay in provider-specific surfaces.

## Platform strategy

| Target | Filesystem | Notes |
| --- | --- | --- |
| Linux | go-fuse | Requires `/dev/fuse` and FUSE 3 tools |
| macOS | Native FSKit app extension | User-space Swift adapter linked to the Go `c-archive`; no macFUSE or kernel extension |
| Windows | WinFsp through cgofuse | Planned adapter; provider and CLI already cross-build |
| Android/iOS | No mount | Reuse provider, VFS, and application services behind a native host |

FUSE is not the product boundary. The provider and VFS layers remain usable by the native FSKit and planned WinFsp adapters and by a mobile GUI that browses and transfers files without mounting.

## Provider distribution

Built-in providers are compiled into the binary and registered in `internal/providers`. This keeps dependency and security behavior reviewable across all supported platforms.

Go's dynamic `plugin` package is not a portable distribution mechanism, so FuseDock will not depend on it. Third-party providers should have two supported paths:

1. Add a Go package and register its factory in a build.
2. Implement a future provider sidecar protocol over an authenticated local transport.

The sidecar protocol is not part of this release. It must preserve stream cancellation, byte ranges, error classes, and secret isolation before it becomes stable; an unauthenticated local socket or arbitrary shell command would not meet that bar.

## Security boundaries

- Provider names and IDs are untrusted input from remote services. Path components are validated before they reach the driver.
- Local-provider access uses `os.Root`, which confines symlink resolution to the configured root.
- PKU authentication only redirects between configured HTTPS origins. Loopback HTTP is allowed solely for local fixtures.
- Passwords, OTPs, cookies, access tokens, and signed storage URLs are not included in errors.
- Saved-account metadata is owner-only; passwords and provider session state use the host credential store. Secret updates are bound to provider and username and serialized across processes.
- One-shot credential files remain supported for development and for the current sandboxed FSKit preview. A local authenticated account broker is required before the extension can consume the system credential store silently.
- A provider is trusted code once loaded. Runtime provider installation is therefore a deliberate future security feature, not an automatic download path.

## Non-goals for the first slice

- Offline synchronization or conflict merging.
- POSIX ownership, ACL, xattr, and hard-link preservation.
- Sharing, version history, recycle bins, or cloud-native import/export jobs.
- A graphical account manager or dynamic mount supervisor.
- Claiming official affiliation with any supported cloud provider.
