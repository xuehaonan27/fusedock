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
| `internal/providers/baidu` | OAuth device authorization and XPAN namespace/range adapter |
| `internal/namespace` | Account-scoped object keys, resolved mount targets, collision-free node numbers |
| `internal/vfs` | Metadata coalescing, revision-bound reads, optional whole-object verification |
| `internal/blockcache` | Shared memory/disk blocks, byte-budgeted 2Q, independent fill cancellation |
| `internal/daemon` | Sessions, desired mount state, local control/data endpoints, shared cache ownership |
| `internal/mountapi` | Driver-neutral mount interface |
| `internal/platform` | Compile-time adapter selection and host service registration |
| `internal/driver/gofuse` | Reusable go-fuse node/handle translation |
| `internal/driver/linux` | Linux mount and kernel integration |
| `internal/driver/fskit` | macOS 27 FSKit mount orchestration |
| `internal/driver/winfsp` | Windows WinFsp callbacks and mount lifecycle |
| `internal/credentials` | Owner-only credential/configuration file parser |
| `internal/accounts` | Account metadata, OS credential storage, default selection, and session-state persistence |
| `internal/broker` | Authenticated one-use claim and read-only loopback object protocol |
| `internal/providers/broker` | Sandbox-safe provider client; secrets never cross into the extension |
| `macos/FSKit` | Swift FSKit app extension and Xcode project |
| `macos/bridge` | C ABI and Go `c-archive` for the FSKit extension |
| `internal/cli` | Provider discovery, inspection, and mount orchestration |

## Provider invariants

A `provider.Session` represents one authenticated account and one namespace.

1. `Root` returns a directory object and does not perform a vendor-specific path lookup.
2. Every object ID is non-empty and unique within its account/provider namespace. Durable inode and data caching require it to remain stable across session restarts for the same account binding.
3. The current `Version` field changes whenever file content or relevant metadata changes. Providers without a native revision synthesize one from timestamp and size. This is only a freshness hint. The target contract splits strong `ContentVersion` from optional `MetadataVersion`; a synthesized value can never authorize a persistent data-cache hit.
4. Every child's `Name` is a single path component. `/`, `\`, NUL, `.` and `..` are invalid. Duplicate names in one listing are an error.
5. `List` returns a complete snapshot for one directory. Provider pagination stays inside the adapter.
6. `OpenRead` returns a handle whose `ReadAt` accepts the caller's context on every call. It must not retain the open call's context.
7. `ReadAt` either returns requested bytes or an error. It must not silently return zeros for missing remote ranges.
8. A session is safe for concurrent `Stat`, `List`, and `OpenRead` calls unless its documentation explicitly states otherwise.

Names are presentation values. The driver may cache or revalidate attributes, but identity, reads, and future mutations use `Object.ID`. This prevents a renamed or shared vendor object from accidentally resolving to a different file.

Common errors (`ErrNotFound`, `ErrPermission`, `ErrConflict`, and others) are translated once by the driver. Vendor HTTP codes and signed URLs never become FUSE errno values directly.

The 2026-10-26 review corrected an important scope: within one account/provider/object ID, equal strong ContentVersion means equal size and bytes; different objects may share a version token. Revision binding applies to every remote request, including URL renewal and retries, not only the initial open. See [design-review-response.md](design-review-response.md).

## Read path

The metadata cache stores short-lived directory snapshots and attributes. Concurrent requests for the same directory share one provider request, and the waiting caller can cancel without cancelling a useful request for another caller.

An open file has one read handle. The VFS layer turns driver-size reads into 4 MiB aligned provider ranges and retains the most recently loaded block per handle. This removes repeated authentication and signed-URL work for ordinary sequential reads. It is an in-memory per-handle cache, not a durable disk cache. The current storage is unbounded when many handles exist; it must move behind a configurable global memory budget before persistent caching or large Finder workloads.

The proposed persistent block cache is specified in [persistent-block-cache.md](persistent-block-cache.md). It requires one daemon owner, stable account/object identity, and a strong ContentVersion. Range fills prove the requested version on every remote request. Whole-file fills may instead verify a provider-declared digest from the open snapshot before delivery and publication; that path is independent of remote range support. It must not be enabled for Baidu range reads until that path is revision-bound.

PKU performs two requests for a cold range: `osdownload` obtains a signed object request, and the object storage returns that exact byte range. The adapter verifies `Content-Range` and the selected revision size before exposing bytes. Signed URLs and their query strings are omitted from errors.

The kernel can issue reads after the handle's original open request has returned. Passing context per `ReadAt` is therefore part of the provider contract.

## Filesystem semantics and read lifetime

The target contract retains immutable read snapshots as a deliberate filesystem consistency model. Selecting a snapshot fixes the handle's content version and size until close/reclaim. A later open can select a refreshed revision. Rename changes a path, while replacement at the same path resolves a different object. A configured mount scope is resolved on each mount and held as an object during that mount.

Every native adapter must preserve these semantics through its own handle/item model and data caches. Concurrent revisions of one object, snapshot attributes, EOF, reconnect, close, and native invalidation are part of acceptance. Normal platform caching is the design path; direct I/O is not a prerequisite. FSKit now uses opaque read handles with explicit close/reclaim. Its macOS 27 cache lifecycle implementation awaits native verification.

Resource accounting covers retained blocks, fill/IPC buffers, metadata, and node/reader tables. The target policy is byte-budgeted 2Q with bounded ghost history, correlated-read accounting, and adaptive bounded read-ahead. Provider fetch size and persistent block size are independent controls. Core counters measure network bytes, first-byte delay, requests, and retained/in-flight resources.

## Account and cache identity

The identity model is specified in [namespace-contract.md](namespace-contract.md). The target account format has an immutable random ID and stable CredentialRef in addition to its display alias. New bindings use AccountID-based credential references. Existing-account conversion is deferred and will retain the current credential locators without moving system credential items. Changing the alias preserves that ID; changing the provider binding creates a new one. A provider object is scoped by account ID and provider ID, while its displayed path is only presentation.

The implemented persistent cache key includes:

```text
account ID + provider ID + object ID + content version + file size + byte range
```

Every daemon VFS session supplies its AccountID and ProviderID explicitly to cache keys. The mounted scope determines authorization and volume identity; it does not change remote content identity. Multi-account mounts will add a virtual first-level account directory and scope IDs before they reach a provider. A shared library must not be confused with a local copy merely because two accounts can see the same name. A bare `Object.ID` is not sufficient for an inode or cache key once two accounts can expose the same vendor ID.

The read cache is not a synchronization cache. A changed version causes a new range; it does not merge local and remote contents. A cache hit is valid only after the caller has resolved an ObjectRevision from a trusted session.

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
| Windows | WinFsp through cgofuse | Callback adapter implemented; native acceptance uses a Windows Actions runner |
| Android/iOS | No mount | Reuse provider, VFS, and application services behind a native host |

FUSE is not the product boundary. The provider and VFS layers remain usable by the native FSKit and WinFsp adapters and by a mobile GUI that browses and transfers files without mounting.

The WebDAV implementation has been deleted. A fresh WebDAV surface and client provider are lowest-priority product work, alongside existing-account migration.

## Compile-time platform routing and complete service delivery

The shared provider/namespace/VFS/cache/daemon contracts contain no native driver types. A common platform interface is implemented by platform-constrained Go files: `platform_linux.go` selects the Linux adapter, `platform_darwin.go` the FSKit host/bridge, and `platform_windows.go` WinFsp. Swift FSKit remains inside the macOS adapter boundary. Reusable go-fuse translation logic is separate from Linux mount and kernel integration.

Core contracts, go-fuse library behavior, Linux native FUSE, macOS FSKit, and Windows WinFsp have independent test targets. Native adapters reuse behavior tests and own their environment setup. Cross-builds remain a build check rather than a mount result.

The complete daemon acceptance contract includes control/data APIs, peer authentication, startup/restart, mount restoration, reauthentication, cache ownership, diagnostics, and shutdown. Current code implements OS-user-authenticated control, one-use data endpoints, shared caching, service registration, and mount restoration. Signed macOS peer reconnection, forced shutdown, complete resource-table accounting, and end-to-end request diagnostics remain open implementation items. Connection, volume, and read recovery retain separate acceptance cases in the same delivery.

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
- The sandboxed FSKit extension consumes accounts through the read-only broker. The broker's bootstrap capability can be claimed once; its session capability exists only in memory. The direct credential-file fallback remains for development.
- A provider is trusted code once loaded. Runtime provider installation is therefore a deliberate future security feature, not an automatic download path.

## Product work outside the read-only foundation

- Offline synchronization or conflict merging.
- POSIX ownership, ACL, xattr, and hard-link preservation.
- Sharing, version history, recycle bins, or cloud-native import/export jobs.
- A graphical account manager.
- Claiming official affiliation with any supported cloud provider.

## Runtime and verification requirements

Go 1.25 is the minimum toolchain because persistent storage uses `os.Root.Rename` and `os.Root.Chtimes` for confined publication and recency updates. The native macOS host requires macOS 27 and Xcode 27. The Go bridge can be built on the current macOS 26 machine; its successful build does not establish that the Swift FSKit extension compiles against the new SDK. See [foundation status](foundation-status.md) for observed results.
