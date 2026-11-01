# FuseDock engineering plan

Updated: 2026-11-01. This is the authoritative implementation plan. The design review and experiments remain evidence; the decisions below supersede their earlier implementation ordering.

FuseDock provides a filesystem abstraction. Its public behavior covers paths, object identity, open/read/close, attributes, random access, errors, and native caching. A read snapshot is a valid filesystem consistency model. The core keeps version-bound snapshots; each platform adapter must implement their observable behavior across the entire read path.

The work below is grouped by responsibility, not implementation stages. Deliver the filesystem foundation as a complete implementation: account identity, namespace, provider capabilities, daemon, platform integration, resource management, and persistent caching are designed and accepted together. The daemon includes its full lifecycle and control/data APIs. Tests are separated by dependency and platform, not by reduced product scope.

## Implementation evidence

The current code and test results are recorded in [foundation status](docs/foundation-status.md). Checked items below identify implemented and locally verified responsibilities; the complete foundation is still open while native acceptance and the listed implementation work remain outstanding.

The user authorized GitHub Actions runners and the public repository `xuehaonan27/fusedock`. On 2026-11-01 the user requested local use on macOS 26.5.1. Keep a macOS 26 FSKit compatibility implementation with remount-based refresh and a separate macOS 27 cache-invalidation implementation. Both share the same core and account broker. Xcode 26 builds compatibility; Xcode 27 builds both with runtime selection.

## Filesystem behavior and read lifetime

- [x] Define the lifetime of a read snapshot from open through close/reclaim, including the operation that selects its revision and size.
- [ ] Bind every read on an exact-version handle to that snapshot. A missing revision returns `ErrConflict`; a handle that has returned bytes never switches versions.
- [ ] Map the snapshot model to Linux file handles, FSKit item/open lifetimes, and WinFsp handles, including concurrent opens of different revisions of the same object.
- [ ] Keep snapshot size, EOF, read results, and handle attributes consistent. Path metadata may describe the newly resolved version while an existing handle still reads its selected version.
- [ ] Replace FSKit's indefinitely retained object-ID reader map with explicit open/close/reclaim ownership. Closing and sequentially browsing files must release readers and buffers.
- [ ] Integrate native data-cache invalidation and refresh with version selection. Use normal platform caching; direct I/O is not an implementation prerequisite or default workaround.
- [ ] Revalidate on reopen according to the provider's measured metadata policy. Define explicit refresh and invalidation behavior.
- [ ] Cover rename, replacement at the same path, same-size content changes, concurrent revisions, cross-block reads, EOF, close/reopen, and resource reclamation in shared surface contracts.
- [ ] Keep ordinary current-version access explicit for providers without snapshot guarantees. That capability does not become proof for a version-keyed persistent entry.

Acceptance: application-visible bytes and attributes follow the selected read contract through provider, daemon, adapter, and native caches; a reopen observes refreshed content; closed reads release their resources. Snapshot semantics are retained as a deliberate filesystem design, rather than inferred from what generic `open()` guarantees.

macOS 26 compatibility contract: the kernel item lifetime fixes the observed file version. Reopening that item retains its version for revision-capable providers; a current-content provider reports `ESTALE` if the opened version differs. Refreshing the mounted view requires unmount, daemon metadata refresh, and mount. The native compatibility suite checks this behavior separately from macOS 27 active invalidation and concurrent-revision acceptance.

## Account identity and namespace

- [x] Give each new saved binding an opaque random `AccountID`, independent of its alias, credentials, and mount path.
- [x] Store a stable `CredentialRef` separately from the alias. New accounts use an AccountID-based reference. Renaming an account changes presentation only.
- [x] Declare provider binding fields explicitly: provider, normalized endpoint, remote principal, and identity-affecting options. Store their versioned fingerprint beside AccountID.
- [x] Preserve identity for credential rotation within the same binding; allocate a new AccountID for a different binding or a recreated account.
- [x] Resolve `NodeKey = account + provider + object` and `ObjectRevision = NodeKey + ContentVersion + size` from trusted session metadata.
- [ ] Validate aliases and remote names under the surface's case/Unicode rules; report collisions without merging objects.
- [x] Resolve a configured scope path at mount time. Keep the resolved root identity for that mount; resolve the configured path again on remount. File rename preserves object identity; replacement creates a different resolved object.
- [x] Derive deterministic volume identity from AccountID and configured scope; keep object/cache identity separate from volume and mount identity.
- [ ] Bind data authorization to the resolved mount target. Account removal or rebinding revokes its capabilities and existing reads.
- [ ] Exercise identical remote IDs in different accounts, path replacement, alias rename, account recreation, and concurrent mounts.

Acceptance: aliases and paths can change without confusing objects or credentials; two accounts never share cache/node identity by accident; the mounted root and each requested object belong to the authorized target.

Existing-account conversion is the lowest-priority compatibility feature at the end of this plan. It is not part of core identity acceptance.

## Provider content proof

- [x] Implement separate `ContentVersion` and optional `MetadataVersion` fields in the provider contract and broker wire format.
- [x] Use the object-scoped invariant: for a given NodeKey, the same non-empty ContentVersion denotes the same size and bytes. Different objects may share a version token.
- [x] Add optional revision and read-capability interfaces. Factory capabilities are an upper bound; sessions and individual objects select the supported paths.
- [x] Define separate eligibility for exact range fills and whole-object declared-digest fills. Whole-object verification does not require remote range support.
- [ ] Bind each request of an exact range reader to its snapshot, including retries, redirects, signed-URL renewal, and metadata operations that select the download revision.
- [x] For conditional ranges, use strong `If-Match` and verify `206`, `Content-Range`, total size, and response validator. A violated condition is `ErrConflict`.
- [x] Keep whole-object digest verification optional: provider metadata supplies the digest, the digest is part of ContentVersion, and the comparison uses the original open snapshot. Download and verify the complete object before delivering bytes through this verified path or publishing its cache entry. Mismatch is `ErrConflict`.
- [ ] Test digest modification, duplicate-content, and concurrent-change semantics before enabling that provider/object path. Locally computing a digest alone supplies no remote version evidence.
- [x] Allow slices of a verified whole-file entry to serve random reads. That entry gives no eligibility to independently fetched unverified remote ranges.
- [ ] Use provider-specific fixtures for PKU rev renewal/rollback/online edits and Baidu canonical-digest objects. Preserve the observed distinction between canonical and non-hex Baidu fields.
- [ ] Document provider-native documents, shortcuts, exports, and unknown-size objects as explicit kinds/capabilities.
- [ ] Assign network retries and `Retry-After` to providers; define a single error mapping for filesystem surfaces. No handle switches revision after returning bytes.

Acceptance: exact range and whole-object digest proofs have separate positive and negative tests; a failed proof publishes no persistent entry; the full read snapshot remains coherent. Whole-file hashing is not a mandatory cost of opening an ordinary file.

## Complete daemon and service lifecycle

- [x] Deliver one daemon owning sessions, namespace state, VFS, persistent cache, and data endpoints.
- [ ] Implement the complete authenticated local control API: account/session state, mount lifecycle, cache operations, diagnostics, reauthentication, shutdown, and service status.
- [x] Implement a versioned binary data plane with target-scoped, revocable access. Resolve authoritative objects server-side from NodeKey/revision requests.
- [ ] Implement reconnectable peer authentication and endpoint discovery through the platform transport: signed XPC/UDS integration on macOS and OS-identity-aware local transports on Linux/Windows.
- [ ] Integrate service startup, login/restart recovery, configured mount restoration, orderly shutdown, and explicit force-stop with each platform host.
- [ ] Restore authenticated operation after reauthentication while preserving mount identity and the contract of existing read snapshots.
- [ ] Handle daemon restart, extension/driver restart, transport loss, and account reauthentication as distinct lifecycle events. Report whether existing reads resume or fail with the defined error; fresh reads use the restored session.
- [ ] Carry request IDs through surface, daemon, VFS, and provider; expose bounded diagnostics without secrets, signed URLs, or file contents.

Acceptance: the complete service runs independently of the initiating CLI; configured mounts and session operations recover as specified; old reads retain their revision or return the defined error; control and data authorization remain separate. Connection establishment alone is not mount or handle recovery.

## Platform isolation and native tests

Target dependency layout:

```text
provider / namespace / VFS / cache / daemon contracts
                          |
                common platform interface
                          |
       +------------------+-------------------+
       |                  |                   |
platform_linux.go  platform_darwin.go  platform_windows.go
       |                  |                   |
 Linux adapter       FSKit host/bridge     WinFsp adapter
       |                  |                   |
    go-fuse          Swift extension         WinFsp
```

- [x] Use Go build constraints and platform filename suffixes to route the common interface at compile time. Keep platform SDK types and driver imports inside their adapter packages.
- [x] Separate reusable go-fuse translation/library logic from Linux mount, `/dev/fuse`, and `fusermount` integration.
- [x] Give core/provider contracts, go-fuse library behavior, Linux FUSE, macOS FSKit, and Windows WinFsp independent test targets and CI jobs.
- [x] Reuse filesystem behavior cases across native adapters through a shared test interface; keep environment setup and native assertions in platform suites.
- [ ] Replace hash-derived FUSE identities with a collision-safe NodeKey map. Verify StableAttr, protocol node ID, `st_ino`, readdir `d_ino`, and FORGET lifecycle separately.
- [x] Reproduce go-fuse's equal-StableAttr deduplication through `fs.NewNodeFS`/raw Lookup on macOS: distinct objects produced node IDs 2 and 2, inode 424242. This library behavior needs no mounted filesystem.
- [x] Move the library regression into its independent test target; verify the NodeKey allocation prevents distinct objects receiving equal StableAttr.
- [x] Keep FSKit `supportsPersistentObjectIDs = false` until there is a durable reverse-resolvable mapping.
- [ ] Implement native-cache/snapshot coherence and open/close/reclaim behavior in each adapter. Restore persistent FSKit IDs only with a durable mapping and lookup tests.
- [x] Run actual Linux and Windows directory/read/reopen/snapshot/EOF/rename-observation/unmount contracts on GitHub Actions runners. [Passing run](https://github.com/xuehaonan27/fusedock/actions/runs/41038439790).
- [x] Build the Swift FSKit app and Go bridge using Xcode 27.1 and the macOS 27 SDK in Actions.
- [x] Validate Xcode 26 compatibility builds and local native remount-refresh acceptance for revision and current-content providers. Keep Xcode 26/27 CI builds separate.
- [x] Isolate macOS 27.2 root invalidation in a standalone Swift FSKit volume. Root/child comparisons across creation order, IDs, coherency, actions and resource types reproduce the failure; kernel logs and read-only analysis locate the missing root lookup. Keep the [reproduction and issue report](docs/fskit-root-cache.md) with a runnable native probe.
- [ ] Complete macOS 27 native acceptance. On the upgraded 27.2 host, the signed SDK 27 extension is enabled and nested directory/read/reopen/snapshot/EOF/same-size-change/rename/unmount tests pass. The FSKit/LIFS root lookup returns `NSOSStatusErrorDomain -536870206`; reopening a changed root-level file still reads the old revision. Resolve that path while retaining the mount layout and old descriptor snapshots, then pass both standalone and root/nested native tests. See [evidence](docs/foundation-status.md#macos-27-native-findings).

Acceptance: the core has no native driver dependency; platform builds select exactly their adapter; library tests do not require a native mount; each native test result identifies its OS and driver. Cross-compilation is a separate build check.

## Global resources, 2Q cache policy, and measured I/O

- [ ] Account separately for retained data, in-flight fill buffers, IPC buffers, metadata lists, node/reader tables, and temporary disk files.
- [ ] Enforce byte budgets and global request/fill concurrency. Admission happens before allocation; cancellation releases reservations and unused work.
- [ ] Implement byte-budgeted 2Q: first-use FIFO, protected reusable entries, and bounded ghost history. Account for headers, payloads, and metadata overhead.
- [x] Group correlated reads from one sequential access when recording reuse; thousands of small reads within one block must not promote a scan into a long-term hot set.
- [ ] Separate provider fetch granularity from persistent block size. Grow bounded read-ahead for sequential access and reduce it after seeks; cancel unused speculative work.
- [ ] Add call, network-byte, latency, queue-time, cancellation, and resource counters at the common provider/VFS boundary.
- [ ] Make metadata TTL configurable per provider/session and expose explicit invalidation. Determine defaults from measured freshness and request costs.
- [ ] Measure Finder open/scroll activity over 60 seconds, QuickLook and media seeks, cold/warm reads, and bounded provider rate-limit behavior. Treat the 8.84 s cold PKU sample as preliminary evidence.
- [ ] Test 1/10/100/1000 concurrent readers, sequential open/close of many files, large directories, slow providers, cancellation, and repeated scans mixed with a hot working set.
- [ ] Verify `.metadata_never_index` behavior at a normal user mount; measure the remaining Finder/QuickLook demand rather than assuming the marker prevents it.

Acceptance: controlled allocations stay within their budgets; sequential browsing releases read state; scans do not evict the reusable working set as plain LRU would; report read amplification, first-byte latency, hit rate, and process-memory overhead. Read-ahead, block size, digest threshold, and TTL have measured defaults.

## Persistent storage and read delivery

- [ ] Implement [the cache design](docs/persistent-block-cache.md) under one daemon owner and one shared VFS.
- [x] Key entries by AccountID, ProviderID, ObjectID, ContentVersion, size, and range; use namespace-tagged fan-out directories.
- [ ] Retain the five-rule declared-digest path alongside exact range fills; select it by object capability and measured whole-file policy.
- [ ] Coalesce concurrent fills with independently cancellable waiters; reserve memory and temporary disk capacity before starting work.
- [x] Store complete temporary entries and publish by atomic rename. Validate header, length, key, and payload on first hit; remove corruption and refill through the same verified read contract.
- [x] Rebuild byte usage and recency by background stat-only scanning. Permit valid hits during scanning; keep new disk allocation within known capacity.
- [x] Recover approximate recency from batched mtime updates; rebuild 2Q reuse/ghost history in memory. No database is required for the initial block index.
- [ ] Use confined file opens and platform-specific ownership/link rules. Implement Windows file-sharing/deletion behavior inside the storage adapter.
- [x] Return successfully validated data even when retention fails. Cache write failure changes retention, not the selected read revision.
- [ ] Add info/clear/prune/verify controls and per-account persistence policy to the complete daemon API.
- [ ] Test restart hits, concurrent native/CLI reads, corruption, torn entries, byte budgets, pinned entries, cleanup, and account isolation.

Acceptance: an unchanged resolved revision can reuse persisted bytes after daemon restart; changed revisions select different entries; cache failures preserve valid reads; native snapshots remain coherent through warm hits. Native platform and provider results are reported independently within the complete delivery.

## Evidence carried forward

| Evidence | Current result | Remaining work |
| --- | --- | --- |
| PKU/Baidu login and reads | Real-account CLI and FSKit paths exercised | Full new read-contract acceptance |
| Keychain write churn | Unchanged state no longer rewrites credentials | Preserve this behavior in service integration |
| FSKit persistent IDs | Declaration set to false; mount/list passed | Native identity and lifetime tests |
| Extension termination | Mounted child returned ENOENT | Complete lifecycle recovery; distinguish connection, volume, and read recovery |
| go-fuse duplicate StableAttr | Independent library and allocator/generation regressions pass | Native inode/FORGET verification |
| Native Linux/Windows mounts | Both passed real directory/read/snapshot/EOF/rename/unmount contracts in Actions | Additional inode/FORGET/replacement coverage; macOS native acceptance |
| Baidu MD5 | 116/823 canonical values; five downloaded samples matched | Mutation, duplicates, concurrency; object-specific eligibility |
| Finder/read latency | Preliminary CLI and small-read timings | Instrumented workloads and measured policy |
| WebDAV | Implementation and tests deleted on 2026-10-28 | Lowest-priority independent rewrite |

## Other product work

- [ ] Aggregate multi-account views with per-account sessions, identities, and quotas.
- [ ] Add Google Drive, OneDrive, S3, and investigate an iCloud integration based on an explicit supported access mechanism.
- [ ] Define safe mutations, expected-version checks, private staging, publication, and recovery journals; cross-account rename is `EXDEV`.
- [ ] Add native GUI/mobile hosts over the shared service contracts.
- [ ] Add signing, notarization, packaging, updates, and the authenticated external-provider SDK.

## Lowest priority: WebDAV and existing-account migration

These features are placed after the filesystem, daemon, cache, platform, and other product work. Neither is a prerequisite for that work.

- [x] Hard-delete the existing WebDAV implementation, tests, broker route, CLI options, and documentation. Leave no compatibility stubs, disabled branches, or replacement implementation.
- [ ] Reimplement WebDAV as an independent consumer of the final namespace/VFS contract. Define authentication, ETag, ranges, and client acceptance in that new implementation.
- [ ] Add a WebDAV client provider as separate protocol coverage.
- [ ] Convert existing v1 saved accounts when that compatibility feature is implemented. Preserve current credential locators as stable CredentialRef values while atomically adding AccountID to account metadata; new accounts use AccountID-based references.
- [ ] Test existing-account conversion with a fake credential store. Conversion does not move/rewrite Keychain items or require a cross-store migration journal.

Existing saved accounts and system credential items were not modified during this implementation work.
