# FuseDock TODO

This is the prioritized engineering backlog. It records the next product stages; only the 0.1 baseline described in the README is currently implemented.

The 2026-10-26 external review and our disposition are in [FuseDock 设计审阅意见.md](FuseDock%20设计审阅意见.md) and [docs/design-review-response.md](docs/design-review-response.md). The review corrected the content-version scope, moved revision binding to every remote request, and made the FSKit lifecycle/control plane a prerequisite for persistent identity.

The first prerequisite-test round and its evidence are recorded in [docs/prerequisite-test-results.md](docs/prerequisite-test-results.md). A gate is removed from this plan only after it passes; a failed or inconclusive experiment remains an explicit blocker.

The immediate dependency order is:

1. Correct the version contract and remove unsafe platform claims immediately.
2. Make account binding and credentials survive rename and restart.
3. Introduce a supervised daemon with a reconnectable authenticated control/data plane.
4. Prove per-request revision binding and provider capabilities.
5. Fix NodeKey, FUSE/FSKit identity, volume identity, and mount lifecycle.
6. Establish metadata, memory, and provider-call baselines.
7. Add the persistent block cache and then aggregate multi-account views.
8. Broaden providers and native platform coverage.
9. Add version-checked writes and recovery.
10. Add distribution, security hardening, and the external provider SDK.

Do not enable a shared disk cache before the namespace contract is implemented and tested. Do not start the write path before the persistent cache and identity model are stable.

## Verification gates and current results

| Gate | Current result | Plan treatment |
| --- | --- | --- |
| FSKit persistent object IDs | Passed for immediate guardrail | `supportsPersistentObjectIDs` is now `false`; a PKU volume still installs, mounts, and lists. Restore persistent IDs only with a daemon-owned mapping. |
| WebDAV recursive PROPFIND | Passed | `Depth: 1` returns 207 and `Depth: infinity` returns 403. Keep the regression test. |
| Repeated macOS security prompts | Passed for unchanged state | Lazily refresh credentials, skip unchanged state writes, and stop rewriting ACLs. Real Baidu and PKU `ls` completed after rebuild without a prompt. AccountID-keyed items remain the long-term cleanup. |
| FSKit extension restart | Failed, blocking | Killing the extension left the mounted child returning `ENOENT`; the one-use capability did not reconnect. This is why UDS/XPC and signed peer identity remain on the critical path. |
| Baidu digest eligibility | Failed for default caching, partial evidence | Of 823 scanned files, 116 had canonical 32-hex MD5 values and 707 did not. Five canonical samples matched download, repeat read, and range bytes. Keep persistent caching off; test duplicate content and remote mutation next. |
| Cold/warm FSKit read | Preliminary only | A PKU PDF's first 1 KiB read took 8.84 s; the immediate repeat was effectively instant. This shows 4 MiB fill amplification on cold reads, not enough evidence to lock block size or read-ahead. |
| Folder listing latency | Preliminary only | Three CLI root listings took 2.74–3.71 s for Baidu and 3.45–3.67 s for PKU including process startup and login restore. No Finder relist or provider-call count exists yet. |
| Metadata indexing protection | Inconclusive | The temporary `/tmp` FSKit mount reported indexing disabled, which may not represent a normal home-directory mount. Test `.metadata_never_index`, Spotlight, and QuickLook at a real mount before claiming protection. |
| Linux FUSE identity | Blocked by environment | This macOS machine has no `/dev/fuse`, `fusermount3`, Linux VM, or container runtime. Cross-build passes; native `st_ino`, readdir `d_ino`, lookup/getattr, and remount behavior still require a Linux host or CI runner. |
| In-memory cap | Not implemented | Per-handle blocks remain unbounded. Implement the global budget and a thousand-handle memory test before persistent caching. |

## 0.2 Namespace and local service foundation

### A0. Contract correction and immediate guardrails

- [x] Replace the documented global version invariant with the object-scoped rule: for one `AccountID + ProviderID + ObjectID`, equal non-empty `ContentVersion` implies equal size and bytes; different objects may share a version token.
- [ ] Implement the object-scoped ContentVersion model in the provider API and broker wire format.
- [x] Change the FSKit extension to declare `supportsPersistentObjectIDs = false` until a daemon-owned persistent `NodeKey` mapping exists.
- [ ] Replace unbounded per-handle blocks with a global, configurable memory budget; retain no more than one useful block per active handle under that budget.
- [ ] Make metadata TTL configurable per provider/session and add an explicit invalidation command. Do not keep a fixed 2-second remote default without measurements.
- [ ] Render a synthetic `.metadata_never_index` on macOS when it does not collide with a remote object, then verify Finder, Spotlight, and QuickLook behavior on a real FSKit volume.
- [x] Reject WebDAV `PROPFIND` with `Depth: infinity`.
- [x] Stop redundant credential-store writes and PKU's unconditional token refresh; verify unchanged real accounts do not prompt on every command.
- [ ] Add regression tests proving no cache is enabled for a provider or object that lacks the required revision guarantees.

Acceptance: FSKit no longer claims persistent IDs it cannot resolve; memory stays within the configured cap during a synthetic thousand-handle workload; a remote provider is not queried for every metadata access solely because of a hard-coded TTL; metadata indexing and WebDAV recursive walks cannot trigger an unbounded scan.

### A. Account binding identity

- [ ] Implement [docs/namespace-contract.md](docs/namespace-contract.md).
- [ ] Add an opaque random `AccountID` to each saved account; never derive or reuse it.
- [ ] Migrate the account store atomically to the new version and fail closed if migration cannot complete.
- [ ] Store each credential item under `AccountID` and migrate existing Keychain/Secret Service/Credential Manager items by local alias; account rename then no longer moves secrets.
- [ ] Add a versioned, non-secret binding fingerprint beside AccountID. Enumerate provider-specific binding fields explicitly and verify the fingerprint at load time.
- [ ] Preserve an account ID for credential/state rotation with an unchanged binding; rotate it only when the declared binding fields change.
- [ ] Treat a fingerprint mismatch as a fail-closed account-state error that requires explicit reauthentication; do not silently reuse the old namespace.
- [ ] Enforce case-insensitive, cross-platform account aliases and reserve FuseDock's virtual names.
- [ ] Test migration rollback, credential-item collisions, renamed aliases, and re-created accounts.

Acceptance: an unchanged binding keeps its ID across restart and reauthentication; renaming leaves credentials and namespace accessible; changing any declared binding field rotates the ID and invalidates old capabilities and cache entries; a fingerprint mismatch blocks the account instead of reading through it.

### B. Reconnectable daemon and control plane

- [ ] Add a long-running local daemon that owns provider sessions, VFS state, the block cache, and broker data-plane endpoints.
- [ ] Supervise it as a user login service with launchd on macOS, systemd user units on Linux, and a future Windows service/tray host.
- [ ] Replace the one-use-only FSKit access model with a UDS/XPC connection that authenticates peer code signing/OS identity and can reconnect after extension restart.
- [ ] Make data-capability authorization target-scoped and revocable; do not require the CLI to remain alive for an already configured mount to reconnect.
- [ ] Define a binary data-plane protocol using raw response bodies or explicit length-prefixed binary frames. Never base64-encode file payloads in control JSON.
- [ ] Add a local-only control API for account selection, service status, mount lifecycle, WebDAV enable/disable, cache clearing, and diagnostics.
- [ ] Make the control API versioned and authorize it separately from FSKit, WebDAV, and data-plane credentials. Do not expose account operations over unauthenticated TCP.
- [ ] Restore configured accounts, mounts, and WebDAV endpoints after daemon restart or login.
- [ ] Stop the daemon only after active operations are allowed to finish; provide a force-stop path for host destruction and explicit user actions.
- [ ] Keep logs bounded and redact credentials, cookies, signed URLs, tokens, and file contents.
- [ ] Expose `ready`/`reauth_required` state, last error, active operations, account state, cache statistics, and version data through the local API.
- [ ] Keep mounts alive through reauthentication: surface `EACCES` while credentials are invalid, then resume reads after CLI/GUI login without unmount/remount.
- [ ] Add one request ID across extension/CLI, daemon, VFS, and provider calls; diagnostics must not include file contents or signed URLs.
- [ ] Add loopback port occupancy and stale-endpoint diagnostics. A stale port can produce Finder's misleading NetAuthAgent message even when credentials are correct.

The extension-restart preflight already fails with `ENOENT`, confirming that capability lifecycle is a correctness/availability gate rather than a later security cleanup.

Acceptance: a machine reboot restores the service and endpoints; an FSKit extension killed and restarted reconnects without a CLI running; reauthentication moves the account from `reauth_required` to `ready` without losing the mount; no file payload crosses the control plane as base64.

### C. Provider revision contract and conformance

- [ ] Add optional `RevisionReader` and session-level capability interfaces instead of a parallel long-lived provider SDK v2. Missing interfaces mean current-revision-only behavior and no persistent cache.
- [ ] Let factory capabilities state an upper bound and session probing narrow them. Per-object opt-out must also be possible.
- [ ] Require every remote request from a revision-bound Reader to use the same `ContentVersion`: range GETs, redirects, signed-URL renewal, retries, and metadata refresh included.
- [ ] Return `ErrConflict` when a requested revision no longer exists, when a conditional request is ignored, when the server returns 200 instead of a validated 206, or when total size/range/ETag differs from the requested snapshot.
- [ ] Use `If-Match` with a strong ETag for conditional reads. Do not use `If-Range` as the exact-version gate; a failed `If-Range` may produce a full 200 response.
- [ ] Validate `206`, `Content-Range`, full size, and response ETag on every conditional request.
- [ ] Add optional full-file digest metadata with algorithm and coverage. Treat MD5 as a compatibility/accidental-change check, not a security guarantee.
- [ ] Allow a whole-file cache fill below the configured threshold only after the downloaded digest matches the metadata digest; keep range caching disabled where that proof is absent.
- [ ] Add two-session mutation tests: one session changes or replaces the file while another reads open ranges. Cover every boundary and ensure no handle mixes revisions.
- [ ] Probe fresh and renewed signed URLs, revision rollback, online editing, and restart behavior for PKU.
- [ ] Keep Baidu persistent caching off until its dlink/MD5 path passes equivalent tests. Reuse `FUSEDOCK_LIVE_ACCOUNTS=1` fixture results: only canonical 32-hex fields are candidates, and 707/823 observed fields were not MD5.
- [ ] Add duplicate-content, one-copy-modified, rename, shared-file, and concurrent-mutation tests before enabling even the Baidu whole-file digest path.
- [ ] Update `pkg/providertest` to exercise capabilities, exact revisions, concurrent mutation, short reads, oversized responses, and cancellation.
- [ ] Audit PKU, Baidu, local, broker, and future providers; document capability evidence rather than inferred behavior.

Acceptance: conformance fails a provider that caches a weak version or binds only the initial `OpenRead`; a remote update during a handle read cannot return mixed-version bytes; PKU passes revision-bound range tests; Baidu passes only the explicitly proven whole-file digest path.

### D. Namespace, platform identity, and volume lifecycle

- [ ] Add a resolver that returns `NodeKey` plus the exact `ObjectRevision` from a trusted session.
- [ ] Change broker wire requests to carry NodeKey/object ID and expected revision only; the daemon resolves the authoritative Object from session state.
- [ ] Replace FUSE bare-ID inode hashing with a NodeKey-to-node map, stable within a mount and collision-safe; test go-fuse protocol node IDs separately from user-visible `st_ino` before deciding their relationship.
- [ ] For FSKit, keep `supportsPersistentObjectIDs = false` now. Either retain that until a daemon-owned persistent NodeKey map exists, or implement the map and resolve arbitrary persisted identifiers correctly.
- [ ] Derive FSKit volume UUID as UUIDv5 over a versioned encoding of AccountID and normalized scope; reject a duplicate AccountID+scope mount while allowing distinct scopes.
- [ ] Ensure single-account and aggregate views resolve the same remote object to the same NodeKey.
- [ ] Replace bare provider Object IDs in session maps with namespaced keys and add collision tests across two accounts and two mounts.

Acceptance: identical provider IDs in two accounts cannot alias nodes; a remount of the same account/scope uses the same FSKit volume identity; an extension restart can resolve persisted identifiers only when the configured mapping actually supports it.

### E. Runtime behavior and measured defaults

- [ ] Document retry ownership: providers own HTTP retries and `Retry-After`; VFS does not retry network reads. A one-time pre-byte re-resolution after `ErrConflict` is allowed, never after partial bytes.
- [ ] Add one provider error -> VFS error -> errno/HTTP mapping table. Map read-handle conflict to `ESTALE` and write conflict to `EBUSY`.
- [ ] Add explicit object kinds/capabilities for provider-native documents, shortcuts, exports, and unknown-size objects. Do not expose an unknown-size export as an ordinary file.
- [x] Capture preliminary PKU/Baidu CLI listing latency and FSKit cold/warm small-read measurements in [docs/prerequisite-test-results.md](docs/prerequisite-test-results.md).
- [ ] Add provider-call and byte counters to the shared daemon path before measuring Finder relist and QuickLook behavior.
- [ ] Benchmark per-request latency and range throughput with instrumentation, cache-hit cost, and memory use at 1/10/100/1000 handles.
- [ ] Use those measurements to set block size, read-ahead, in-memory cap, metadata TTL, and whole-file threshold. Record the baseline in the design docs.
- [ ] Run an A/B block-size test against the same cold files and a real Finder/QuickLook mount at a normal home-directory path.
- [ ] Add CLI invalidation for metadata and cache when a user needs an immediate refresh.

Acceptance: defaults have measurements and documented provider-specific values; retries cannot multiply across layers; stale metadata refresh is explicit and bounded; unknown-size/provider-native objects have defined surface behavior.

### F. Persistent block cache

- [ ] Implement the design in [docs/persistent-block-cache.md](docs/persistent-block-cache.md).
- [ ] Make one broker/daemon process the cache owner and pass one cached VFS session to FSKit and WebDAV.
- [ ] Remove duplicate per-handle block caching where it would compete with the shared cache.
- [ ] Store blocks under `entries/<namespaceTag>/<keyHash[:2]>/<keyHash>.blk`; use the namespace tag only for isolation, accounting, and clear.
- [ ] Use a background startup scan for budget/LRU. Until it finishes, behave as if the budget is full and avoid new persistent writes.
- [ ] Validate header and file size at startup, then verify payload SHA-256 once per process on first hit. Track a verified flag and delete/refill on mismatch.
- [ ] Avoid per-block `fsync` and directory `fsync` by default. Atomically rename a complete temp file, detect torn files after crash by header/hash, and provide an optional conservative sync mode.
- [ ] Recover LRU order from file mtime/touch metadata in batches; do not write one access timestamp per small read.
- [ ] Open block files through a confined root with no-follow semantics and validate the opened object, avoiding lstat/open races.
- [ ] Keep a global memory budget and global fill concurrency budget independent of disk budget.
- [ ] Support per-account disable of persistent data caching and per-object opt-out.
- [ ] Permit the small-file whole-file digest path only when the exact metadata digest and downloaded digest agree; never extend that exception to unverified range reads.
- [ ] Add cache info, clear, prune, and integrity-check commands.
- [ ] Add metrics for hit rate, coalesced fetches, network bytes, evicted bytes, corruption, verification cost, and cache write failures.
- [ ] Do not add bbolt for the initial block index; revisit only if measured startup or lookup cost requires it.

Acceptance: reopening an unchanged file after daemon restart serves cached ranges without a provider data call; any unverified revision cannot produce a disk hit; corruption or a torn crash file is removed and safely refilled; daemon startup and cache hits remain responsive at the documented budget.

### G. Stable WebDAV and loopback operations

- [ ] Store the WebDAV Basic-auth credential in Keychain, Secret Service, or Credential Manager instead of regenerating a token on every broker start.
- [ ] Provide explicit rotate/revoke operations and show the active endpoint and credential source in the control UI.
- [ ] Keep a persistent read-only WebDAV URL usable across daemon restarts; keep the fixed loopback port by default once it is available.
- [ ] Preserve exact-origin browser restrictions; never add wildcard CORS.
- [ ] Add connection diagnostics for Finder's ECONNREFUSED/ErrorNumber 61 case and for stale endpoints.
- [ ] Emit a strong ETag only from a strong ContentVersion; otherwise emit a weak ETag or omit it.

Acceptance: Finder can reconnect after a daemon restart using the same saved server entry; a revoked credential stops working; clients cannot mistake a weak version for a strong ETag.

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

- [ ] Run native Linux FUSE acceptance on a FUSE-enabled host or CI runner; the current macOS-only machine cannot execute this gate.
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
- [ ] Keep the FSKit extension able to reconnect after process restart through a signed peer identity; one-use bootstrap files cannot be the only lifecycle mechanism.
- [ ] Add a single security review template covering new providers, access surfaces, cache payloads, and write privileges.
- [ ] Add fuzz tests for WebDAV paths/XML, broker JSON, cache headers, and provider path components.
- [ ] Add performance benchmarks for metadata/relist calls, Finder/QuickLook patterns, cold/warm reads, small-file workloads, large-file ranges, memory use, cache eviction, and concurrent clients.
- [ ] Add structured diagnostics with a user-facing export command and strict secret redaction.
- [ ] Keep Linux/macOS/Windows cross-builds and platform-native tests in CI.
- [ ] Update the user docs and threat model whenever a capability becomes writable, network-visible, or persisted.
- [ ] Add explicit non-goals: offline bidirectional sync, collaborative editing, remote POSIX locks, and transparent cross-provider conflict merging.

## Current safeguards that must remain true

- No account password or refresh token in accounts.json or the data cache.
- FSKit filesystem-extension access remains read-only until the mutation contract exists.
- WebDAV remains loopback-only and rejects unlisted browser origins.
- Provider names/IDs are not trusted as local paths.
- Weak or synthetic versions, metadata fingerprints, and Stat-before/after checks never authorize persistent data caching.
- File payloads never traverse a control protocol as JSON/base64.
- FSKit persistent object IDs remain false until an actual durable mapping exists.
- Signed object-storage URLs never appear in logs or errors.
- One unused or corrupt cache entry can be discarded without failing a valid remote read.
- Cache is an optimization; cache-disk failure must not be reported as successful persistence of user data.
