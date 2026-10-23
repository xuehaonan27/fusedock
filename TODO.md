# FuseDock TODO

This is the prioritized engineering backlog. It records the next product stages; only the 0.1 baseline described in the README is currently implemented.

The immediate dependency order is:

1. Immutable account and object identity under a shared namespace contract.
2. A supervised broker/daemon and authenticated local control plane that owns those namespaces.
3. A persistent, version-safe block cache shared by FSKit, WebDAV, and CLI.
4. Aggregate and scoped multi-account views across native platforms.
5. More providers and native platform drivers.
6. Version-checked writes and recovery.
7. Distribution, security hardening, and the external provider SDK.

Do not enable a shared disk cache before the namespace contract is implemented and tested. Do not start the write path before the persistent cache and identity model are stable.

## 0.2 Namespace and local service foundation

### Identity and namespace resolution

- [ ] Implement [docs/namespace-contract.md](docs/namespace-contract.md).
- [ ] Add an opaque random `AccountID` to each saved account; never derive or reuse it.
- [ ] Migrate the account store atomically to the new version and fail closed if migration cannot complete.
- [ ] Preserve an account ID for credential/state rotation with an unchanged provider binding; rotate it on provider, endpoint, username, or option changes.
- [ ] Enforce case-insensitive, cross-platform account aliases and reserve FuseDock's virtual names.
- [ ] Add a scoped namespace/resolver object containing `NamespaceKey`, `NodeKey`, and the exact `ObjectRevision` used for reads.
- [ ] Require durable object IDs and revision-bound reads before a provider may enable persistent inode or data caching.
- [ ] Derive FUSE and FSKit node identifiers from the full node key, detect collisions, and stop hashing only the bare provider object ID.

Acceptance: an unchanged account keeps its ID across restart and reauthentication; removing and recreating it produces a different ID; identical object IDs in two accounts cannot share an inode or cache namespace; the same object exposed through aggregate and single-account views resolves to the same node key.

### fusedockd lifecycle

- [ ] Add a long-running local daemon that owns provider sessions, VFS state, the block cache, and broker data-plane endpoints.
- [ ] Supervise it as a user login service with launchd on macOS, systemd user units on Linux, and a future Windows service/tray host.
- [ ] Restore configured accounts, mounts, and WebDAV endpoints after daemon restart or login.
- [ ] Stop the daemon only after active operations are allowed to finish; provide a force-stop path for host destruction and explicit user actions.
- [ ] Keep logs bounded and redact credentials, cookies, signed URLs, tokens, and file contents.
- [ ] Expose status, last error, active operations, account state, cache statistics, and version data through a structured local API.

Acceptance: a machine reboot restores the configured service and WebDAV/mount endpoints without requiring a terminal command; a stale session reports one actionable error without leaking credentials.

### Authenticated local control plane

- [ ] Add a local-only control API for account selection, service status, mount lifecycle, WebDAV enable/disable, cache clearing, and diagnostics.
- [ ] Use a peer-authenticated Unix domain socket/XPC transport on macOS/Linux where available. Do not expose account or control operations over unauthenticated TCP.
- [ ] Add request authorization that is separate from the WebDAV data token and the one-use FSKit capability.
- [ ] Make the API versioned and refuse incompatible clients explicitly.
- [ ] Add loopback port occupancy and stale-endpoint diagnostics. A stale port can produce Finder's misleading NetAuthAgent message even when credentials are correct.

Acceptance: a normal user can stop and restart the service from a menu-bar/tray UI, see whether it is running, and recover from an occupied or stale local endpoint without reading system logs.

### Stable WebDAV credentials

- [ ] Store the WebDAV Basic-auth credential in Keychain, Secret Service, or Credential Manager instead of regenerating a token on every broker start.
- [ ] Provide explicit rotate/revoke operations and show the active endpoint and credential source in the control UI.
- [ ] Keep a persistent read-only WebDAV URL usable across daemon restarts; keep the fixed loopback port by default once it is available.
- [ ] Preserve exact-origin browser restrictions; never add wildcard CORS.
- [ ] Add connection diagnostics for Finder's ECONNREFUSED/ErrorNumber 61 case.

Acceptance: Finder can reconnect after a daemon restart using the same saved server entry and a revoked credential immediately stops working.

### Persistent block cache

- [ ] Implement the design in [docs/persistent-block-cache.md](docs/persistent-block-cache.md).
- [ ] Make one broker/daemon process the cache owner and pass one cached VFS session to FSKit and WebDAV.
- [ ] Remove duplicate per-handle block caching where it would compete with the shared cache.
- [ ] Add cache info, clear, prune, and integrity-check commands.
- [ ] Add metrics for hit rate, coalesced fetches, network bytes, evicted bytes, corruption, and cache write failures.
- [ ] Disable disk caching for providers or objects that cannot bind OpenRead to the requested Object.Version.

Acceptance: reopening an unchanged file after a daemon restart serves its cached ranges without a provider read; changing the remote version never returns bytes from the old version; the cache stays within its disk budget.

### Cache-aware provider contract

- [ ] Tighten OpenRead semantics so it serves the supplied object revision or returns ErrConflict; it must not silently read a newer revision.
- [ ] Add provider conformance cases for version changes during an open handle, empty versions, and ReadAt cancellation.
- [ ] Audit PKU, Baidu, local, and future providers against the tightened contract.
- [ ] Keep Baidu persistent caching opt-in until its downloaded bytes can be tied to the metadata revision used for the cache key.

## 0.3 Multi-account and broader access

### Multi-account virtual root

- [ ] Allow one broker session to expose several saved accounts, for example /pku, /baidu, and /onedrive.
- [ ] Give each account an independent provider session, refresh lock, cache namespace, and inode namespace keyed by its immutable account ID.
- [ ] Reject cross-account rename with EXDEV before any remote mutation.
- [ ] Deduplicate shared quota only when account/server/library identity proves it is the same backing store.
- [ ] Support single-account and aggregate views in FSKit, WebDAV, CLI, and future GUI surfaces.
- [ ] Add account removal/relogin while the daemon is running without resurrecting removed sessions.

### WebDAV client provider

- [ ] Implement FuseDock's own WebDAV client as a read-only provider.
- [ ] Handle redirects, credentials, TLS validation, PROPFIND pagination/depth, byte ranges, ETags, and server-specific quirks safely.
- [ ] Use it for Nextcloud, NAS, and services without a native FuseDock provider.
- [ ] Keep proxy behavior explicit and never forward credentials to a redirect origin without validation.

### Additional providers

- [ ] S3-compatible provider with endpoint configuration, list continuation, ETags, range reads, and optional static credentials.
- [ ] OneDrive through Microsoft Graph with OAuth PKCE and stable item IDs.
- [ ] Google Drive with PKCE, shared-drive/shortcut semantics, and an explicit Google Docs export policy.
- [ ] Complete Baidu live coverage for pagination, rate limits, shared files, and cloud-specific file types.
- [ ] Add a provider conformance fixture that exercises every item in the shared runbook.

### Platform drivers

- [ ] Run native Linux FUSE acceptance on a FUSE-enabled host.
- [ ] Re-run macOS FSKit acceptance after daemon deployment and extension updates.
- [ ] Add a WinFsp/cgofuse backend with native Windows mount, range-read, rename, and unmount tests.
- [ ] Add a signed Windows service/tray host and protected cache/credential directory handling.
- [ ] Evaluate a gRPC or CBOR sidecar protocol for non-Go providers only after the native provider contract is stable.

## 0.4 Safe writes

- [ ] Define mutation capabilities and revision semantics separately from the read contract.
- [ ] Stage writes in a private local directory and record the expected remote version.
- [ ] Publish on fsync/close and only then acknowledge durable success.
- [ ] Map version conflicts to EBUSY; never overwrite a newer remote revision silently.
- [ ] Retain failed staged files and recovery journals with correct account/provider/object identity.
- [ ] Support create, overwrite, truncate, append, mkdir, remove, and same-backend rename.
- [ ] Reject cross-account and cross-provider rename with EXDEV.
- [ ] Enable WebDAV write methods only when the selected provider supports them and the token has write capability.
- [ ] Add conflict, interrupted-upload, crash-recovery, disk-full, and concurrent-editor fixtures.

## 0.5 Distribution and ecosystem

- [ ] Add macOS signing, notarization, app bundle release, and FSKit extension upgrade handling.
- [ ] Add Windows signing and WinFsp packaging.
- [ ] Add Homebrew and other installer channels.
- [ ] Add automatic update and rollback paths with signed release metadata.
- [ ] Version and document the local control protocol and provider sidecar protocol.
- [ ] Publish a provider test harness and conformance results.
- [ ] Add crash-safe cache/session migrations that can be rolled back.

## Cross-cutting work

- [ ] Keep all privileged or secret-bearing operations out of the FSKit extension sandbox; use the authenticated broker/daemon boundary.
- [ ] Add a single security review template covering new providers, access surfaces, cache payloads, and write privileges.
- [ ] Add fuzz tests for WebDAV paths/XML, broker JSON, cache headers, and provider path components.
- [ ] Add performance benchmarks for cold/warm reads, small-file workloads, large-file ranges, cache eviction, and concurrent clients.
- [ ] Add structured diagnostics with a user-facing export command and strict secret redaction.
- [ ] Keep Linux/macOS/Windows cross-builds and platform-native tests in CI.
- [ ] Update the user docs and threat model whenever a capability becomes writable, network-visible, or persisted.
- [ ] Add explicit non-goals: offline bidirectional sync, collaborative editing, remote POSIX locks, and transparent cross-provider conflict merging.

## Current safeguards that must remain true

- No account password or refresh token in accounts.json or the data cache.
- FSKit filesystem-extension access remains read-only until the mutation contract exists.
- WebDAV remains loopback-only and rejects unlisted browser origins.
- Provider names/IDs are not trusted as local paths.
- Signed object-storage URLs never appear in logs or errors.
- One unused or corrupt cache entry can be discarded without failing a valid remote read.
- Cache is an optimization; cache-disk failure must not be reported as successful persistence of user data.
