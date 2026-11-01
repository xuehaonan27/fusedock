# Filesystem foundation: implementation and evidence

Updated 2026-11-01. [TODO.md](../TODO.md) remains the complete acceptance plan. This document records the current working tree and observed checks. The filesystem foundation has open acceptance and implementation items.

## Implemented

| Area | Current code |
| --- | --- |
| Accounts | New bindings have random AccountID, stable CredentialRef, normalized binding fingerprints, identity-preserving credential rotation and alias rename, and binding-checked refresh persistence. Existing account records are unchanged. |
| Namespace | Account/provider/object keys, resolved scope roots, target revocation, deterministic volume IDs, sequential collision-free inode allocation. |
| Read contract | ContentVersion and MetadataVersion, session capability negotiation, explicit immutable revision reads, optional whole-object declared-digest verification. |
| PKU | Each exact read and signed-URL renewal carries the original revision. Fixtures verify renewal, strict range responses, and missing-revision conflicts. |
| Baidu | Current-content reads and fresh metadata; canonical declared MD5 is represented in object metadata. Declared-digest persistence is disabled pending modification, duplicate-content, and concurrent-change qualification. |
| Cache | Shared immutable blocks, independent cancellation of coalesced fills, memory reservations, byte-based 2Q, bounded ghost history, correlated-read accounting, pinned leases, persistent restart hits, corruption removal/refill, background stat-only scanning. |
| Disk storage | One-owner lock, private directories, namespace-separated paths, root-relative publication, header/key/size/payload checks, platform file checks, clear/prune/verify. |
| Broker | Protocol v2; server-resolved objects/revisions; binary reads; explicit read/release; single-use bootstrap; revocable in-memory capabilities; per-endpoint handle limits and shared IPC concurrency. |
| Daemon | Shared cache and metadata/IPC concurrency, session pooling, local control, data attachment, saved mount restoration, reauthentication by AccountID, account-removal/rebind observation, lifecycle state/error reporting. |
| CLI | Daemon start/status/mount/unmount/refresh/reauthenticate/stop, ls/cat over daemon data, cache administration, service install/uninstall, account rename. |
| Platforms | Compile-time dispatch to Linux, macOS FSKit, Windows WinFsp. Daemon imports the generic mount contract; adapter selection happens in the host. Reusable go-fuse translation is separate from Linux mounting. |
| FSKit | macOS 26 minimum; separate compatibility OpenCloseOperations and macOS 27 DataCacheHandler classes. SDK conditions and runtime availability select the lifecycle. Shared explicit read handles, close/reclaim release and broker. macOS 26 native remount-refresh acceptance passed; macOS 27 active-invalidation acceptance remains pending. |

## Checks executed locally

Host: macOS 26.5.1, arm64. Default Go: 1.27.3. Xcode: 26.6.

- `make check`: formatting, `go vet`, all Go race tests, Linux/macOS/Windows CLI cross-builds passed.
- `GOTOOLCHAIN=go1.25.0 go test ./internal/blockcache ./internal/daemon ./internal/vfs`: passed.
- Go bridge `c-archive` build: passed locally and in the previous Xcode 27 Actions job.
- Xcode 26.6 unsigned and Apple Development signed app builds: passed. Installed to the existing `~/Applications/FuseDock.app`; PluginKit lists one FuseDock extension.
- macOS 26 native compatibility contract: passed for both revision-capable and current-content-only provider fixtures, including directory listing, simultaneous readers, uncached and cross-block reads, frozen size/EOF, close/reopen, write rejection and refresh by remount. A changed current-content provider reports `ESTALE` on reopen; the remounted view reads the new content.
- Core snapshot sharing exercised 1, 10, 100, and 1,000 concurrent readers. Each immutable revision was fetched once in the fixture.
- Cache tests exercised waiter cancellation, payload reservations, a hot working set mixed with scans, pinned clear, restart hits, corrupt entries, and namespace isolation.
- Daemon fixture tests exercised authenticated control, binary data reads, renamed-account reauthentication, stable volume identity after restoration, and visible native-unmount errors.
- The go-fuse library suite reproduced equal-StableAttr deduplication and verified distinct object allocation and distinct generations for changed revisions.

These tests use fake credential stores and local HTTP/provider fixtures. Windows runs include positive private-file reads, rejection of broad DACLs, and hard-link rejection. Real account credentials were not read or changed. The existing signed app was updated for local compatibility acceptance. No host service registration was performed.

## Native acceptance and CI

The shared native contract mounts a fixture and checks directory enumeration, old/new content, old uncached ranges, cross-block reads, snapshot size/EOF, same-size changes, rename observation, and unmount. Platform suites opt in explicitly:

| Entry | Host requirement | Current result |
| --- | --- | --- |
| `make test-core` | Go toolchain | Passed through the complete Go suite |
| `make test-gofuse` | Linux or macOS; no mount | Passed |
| `make test-linux` | Linux with FUSE 3 and `/dev/fuse` | Passed in the first Actions run |
| `make test-windows` | Windows with WinFsp | Passed in the final code Actions run |
| Xcode build jobs | `macos-26` and `xcode-27` runners | Both builds and SDK compile-condition checks passed |
| `make test-macos` | macOS 26, matching signed and enabled extension | Remount-refresh native contract passed locally |
| `make test-macos` | macOS 27, matching signed and enabled extension | Active-invalidation native contract awaits local OS upgrade |

The public repository is [xuehaonan27/fusedock](https://github.com/xuehaonan27/fusedock). Code commit `7faa61f` passed all ten enabled CI jobs; the subsequent acceptance-record update changes documentation only. [The first Actions run](https://github.com/xuehaonan27/fusedock/actions/runs/41034945547) passed Linux native FUSE, Xcode 27 compilation, Linux/macOS core tests, race tests, go-fuse library tests, and cross-builds. The initial Windows failures led to SID/DACL validation, native mountpoint preparation, and readiness fixes. [The final code Actions run](https://github.com/xuehaonan27/fusedock/actions/runs/41038439790) passed all ten enabled jobs, including Windows core and native snapshot acceptance. That run skipped macOS native mounting. The subsequent macOS 26 compatibility contract has now passed locally; the macOS 27 contract still awaits a local OS upgrade. The SDK 26 build excludes SDK 27 symbols. Cross-build success is recorded separately from native filesystem results.

The macOS 26 compatibility change at `cc201db` passed [all eleven enabled CI jobs](https://github.com/xuehaonan27/fusedock/actions/runs/41293998761), including both Xcode SDK builds, Linux and Windows native mounts, and all core/race/cross-build jobs. The optional self-hosted macOS job was skipped; the macOS 26 native contract was executed locally against the signed installed extension.

## Open work in the same acceptance plan

1. After the user upgrades this Mac, run native filesystem tests for the Swift extension on macOS 27; Xcode 27 compilation has passed. Establish native old/new item, kernel name-cache, data-cache, and EOF behavior. DataCacheHandler implementation alone does not establish that two FSItem instances for one file ID satisfy concurrent-revision semantics.
2. Linux and Windows native contracts passed. Complete additional native inode/FORGET and path replacement coverage.
3. Implement signed macOS peer reconnection and endpoint discovery after extension restart. The current one-use bootstrap does not recover a relaunched extension. Test transport, daemon, extension, and reauthentication recovery separately.
4. Finish explicit forced shutdown, retry after a failed native unmount, and bounded shutdown behavior. Current errors remain visible; a failed native unmount may still leave the OS mount present.
5. Complete accounting/admission for metadata in-flight allocations, object/node tables, reader tables, temporary files, and process overhead. Shared block payloads and request concurrency already have limits; some identity maps still grow with observed objects.
6. Separate provider fetch granularity from persistent block size and implement bounded adaptive read-ahead. Current read blocks are 4 MiB, capped by the memory budget.
7. Carry request IDs across the complete read path and expose bounded diagnostics. Add native workload measurements for Finder, QuickLook, media seeking, rate limiting, and TTL selection. Current TTL and whole-file limits are provisional.
8. Complete per-surface Unicode/case/name collision rules, remote replacement tests, PKU real revision mutation cases, and Baidu digest qualification.

The completed code and these open items belong to the same delivery. WebDAV and old-account migration remain at the end of the product backlog.

## Runtime minimums

Go 1.25 supplies confined `os.Root.Rename` and `os.Root.Chtimes`; see the [Go 1.25 release notes](https://go.dev/doc/go1.25). The app deployment target is macOS 26, where Path URL resources and OpenCloseOperations support the compatibility lifecycle. Xcode 26 builds that lifecycle; Xcode 27+ builds both lifecycles with runtime selection. macOS 27 supplies the FSKit [DataCacheHandler](https://developer.apple.com/documentation/fskit/fsvolume/datacachehandler) and [setCacheState](https://developer.apple.com/documentation/fskit/fsvolume/setcachestate(for:cachemode:coherencytype:action:)) APIs used by the extension.

## Existing accounts and persistence

Mounts requesting persistent retention require a saved AccountID. A legacy account without that field returns an explicit error before credential lookup; it can still be mounted with `--persistent-cache=false`. This work does not rewrite old account records or move their credential items. New saved bindings provide stable cache identity.
