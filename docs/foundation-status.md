# Filesystem foundation: implementation and evidence

Updated 2026-11-02. [TODO.md](../TODO.md) remains the complete acceptance plan. This document records the current working tree and observed checks. The filesystem foundation has open acceptance and implementation items.

## Implemented

| Area | Current code |
| --- | --- |
| Accounts | New bindings have random AccountID, stable CredentialRef, normalized binding fingerprints, identity-preserving credential rotation and alias rename, and binding-checked refresh persistence. Existing account records are unchanged. |
| Namespace | Account/provider/object keys, resolved scope roots, target revocation, deterministic volume IDs, sequential collision-free inode allocation. |
| Read contract | ContentVersion and MetadataVersion, session capability negotiation, explicit immutable revision reads, optional whole-object declared-digest verification. |
| PKU | Each exact read and signed-URL renewal carries the original revision. Validated range responses support progressive prefix delivery. Fixtures verify renewal, strict range responses, and missing-revision conflicts. |
| Baidu | Current-content reads, progressive HTTP range delivery, per-reader download-link reuse/renewal, and fresh metadata; canonical declared MD5 is represented in object metadata. Declared-digest persistence is disabled pending modification, duplicate-content, and concurrent-change qualification. |
| Cache | Shared blocks with independently available immutable intervals, demand-sized fetches, bounded adaptive read-ahead, independent cancellation of coalesced fills, memory reservations, byte-based 2Q, bounded ghost history, correlated-read accounting, pinned leases, asynchronous complete-block persistence, restart hits, corruption removal/refill, background stat-only scanning. |
| Disk storage | One-owner lock, private directories, namespace-separated paths, root-relative publication, header/key/size/payload checks, platform file checks, clear/prune/verify. |
| Broker | Protocol v2; server-resolved objects/revisions; binary reads; explicit read/release; single-use bootstrap; revocable in-memory capabilities; per-endpoint handle limits and shared IPC concurrency. |
| Daemon | Shared cache and metadata/IPC concurrency, session pooling, local control, data attachment, saved mount restoration, reauthentication by AccountID, account-removal/rebind observation, lifecycle state/error reporting. |
| CLI | Daemon start/status/mount/unmount/refresh/reauthenticate/stop, ls/cat over daemon data, cache administration, service install/uninstall, account rename. |
| Platforms | Compile-time dispatch to Linux, macOS FSKit, Windows WinFsp. Daemon imports the generic mount contract; adapter selection happens in the host. Reusable go-fuse translation is separate from Linux mounting. |
| FSKit | macOS 26 compatibility and macOS 27 DataCacheHandler lifecycles. SDK/runtime selection, explicit read handles and broker. macOS 27 root namespace publication uses authenticated native rename/unlink; child directories use cache invalidation. Root and nested snapshot acceptance pass on 27.2; macOS 26 remount acceptance previously passed. |

## Checks executed locally

Compatibility acceptance host before upgrade: macOS 26.5.1, arm64. Default Go: 1.27.3. Xcode: 26.6.

Current host: macOS 27.2 (26B123), arm64. Xcode 27.2 is installed; its license confirmation is pending, so local Go commands use the existing Command Line Tools and app builds use the Xcode 27 Actions runner. The CI app is signed with the existing local development identity/profile and installed to `~/Applications/FuseDock.app`. The minimum OS remains 26.0. FSClient reports the canonical extension enabled; signed replacements preserve enablement.

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
| `make test-macos` | macOS 27, matching signed and enabled extension | Root/nested snapshots, same-size updates, rename, namespace publication, read-only rejection and unmount passed |

The public repository is [xuehaonan27/fusedock](https://github.com/xuehaonan27/fusedock). Code commit `7faa61f` passed all ten enabled CI jobs; the subsequent acceptance-record update changes documentation only. [The first Actions run](https://github.com/xuehaonan27/fusedock/actions/runs/41034945547) passed Linux native FUSE, Xcode 27 compilation, Linux/macOS core tests, race tests, go-fuse library tests, and cross-builds. The initial Windows failures led to SID/DACL validation, native mountpoint preparation, and readiness fixes. [The final code Actions run](https://github.com/xuehaonan27/fusedock/actions/runs/41038439790) passed all ten enabled jobs, including Windows core and native snapshot acceptance. That run skipped macOS native mounting. The subsequent macOS 26 compatibility contract has now passed locally; the macOS 27.2 native results are recorded below. The SDK 26 build excludes SDK 27 symbols. Cross-build success is recorded separately from native filesystem results.

The macOS 26 compatibility change at `cc201db` passed [all eleven enabled CI jobs](https://github.com/xuehaonan27/fusedock/actions/runs/41293998761), including both Xcode SDK builds, Linux and Windows native mounts, and all core/race/cross-build jobs. The optional self-hosted macOS job was skipped; the macOS 26 native contract was executed locally against the signed installed extension.

The installed SDK 27 artifact and source for `981396a` passed [all eleven enabled CI jobs](https://github.com/xuehaonan27/fusedock/actions/runs/41308754383). Local Go race checks for the FSKit driver, broker, VFS, namespace and daemon also passed after the OS upgrade. macOS native mounting is a separate local check, not covered by those eleven jobs.

The standalone probe and runner at `6a28cdd` passed [all eleven enabled CI jobs](https://github.com/xuehaonan27/fusedock/actions/runs/41315605385). The diagnostic Xcode 27 build succeeded separately. The local runner then collected 40 root cache-state failures and 40 successful child calls across ten mounts. After restoring the normal app, the local native result remained root failed, nested passed, remount refresh passed. Seven extracted test app copies from this investigation were unregistered and removed; the canonical app signature verified and FSClient reported its single registered extension enabled.

## macOS 27 native findings

The root publication implementation at `566631d` passed [all eleven enabled CI jobs](https://github.com/xuehaonan27/fusedock/actions/runs/41400083412), including Linux/Windows native mounting and both SDK app builds. After signing and installing that app locally, the original root and nested snapshot tests both passed. The separate root publication test then passed creation, negative-cache refresh, rename, same-size replacement, deletion with a retained old descriptor, directory-to-file replacement and clean enumeration.

Final application code `820e3b4` passed [all eleven enabled CI jobs](https://github.com/xuehaonan27/fusedock/actions/runs/41402004993). Its signed app passed the complete local `make test-macos` suite, including the strengthened directory-to-file test with an old directory descriptor retained. Published root children now retain their presented type through replacement; a direct Stat of the already replaced provider path must not change an existing vnode's type. The host-confinement test verifies that even an authenticated publication cannot rename or unlink files in an unmounted host directory. `go test -race ./...` passed locally. The standalone publication probe also passed 42 opens per directory across B→C, observing both revisions while retained A/B/C descriptors stayed valid.

The user enabled the installed module through **By Category → File System Extensions**. The **By Application** control reproduced the Team ID error described in [the FSKit toggle investigation](https://github.com/macfuse/macfuse/issues/1194). Subsequent signed app replacements preserved enablement.

| Local native check | Observed result on macOS 27.2 |
| --- | --- |
| Root and nested directory listing, file reads, write rejection, unmount | Passed |
| Remount after changing root-level and nested file contents | Passed |
| Nested file reopen after remote content/size change | Passed |
| Old descriptor cached/uncached/cross-block reads, size and EOF after new revision opens | Passed in both root and nested contracts |
| Same-size replacement and remote rename | Passed in both root and nested contracts |
| Root-level reopen after remote content change | Passed with native namespace publication |
| Root creation/deletion, negative cache, type replacement, retained reader after deletion | Passed |
| Ordinary create/mkdir/rename/unlink and forged publication names | Rejected; file content preserved |
| Full macOS 27 native suite | Passed; the macOS 26-only contract is skipped on this host |

The macOS 27 adapter uses `FSVolume.Handler`, `ReadWriteHandler` and `DataCacheHandler`. At `EntryTTL`, the root metadata view is refreshed through real native rename/unlink callbacks; ordinary child directories use `setCacheState(..., action: .invalidate)`. These kernel operations run outside module locks. Files retain their selected read snapshot, size and EOF. [The resolution and authorization review](fskit-root-cache.md#implemented-resolution) describe the one-use publication, resource confinement, root permission bits and metadata-polling cost.

### Earlier failure and independent OS reproduction

A temporary native probe tried five cache-mode/coherency pairs on ordinary files, a nested directory and the root directory. Files and the nested directory returned success for every pair; the root returned `NSOSStatusErrorDomain -536870206` (`0xe00002c2`, `kIOReturnBadArgument`) for every pair. The framework logged `setCacheStateWithLiveFH failed`. The pre-fix adapter reproduced the root failure and passed the nested contract. Its log confirmed `root=true node=2`: the failing item was the activation root. Separate root/nested regression cases are retained.

The subsequent standalone Swift reproduction removes all provider, broker, VFS and block-cache calls. Root creation order, inode and parent IDs, initial coherency, cache actions and file/directory resources reproduce the same root-only failure. Kernel logs report `methodSetCacheState: no lifsnode found for fh`. Read-only analysis of build 26B123 shows the root stored separately from the node hash table searched by the cache-state operation. This identifies the FSKit/LIFS root lookup as the blocking path; Apple has not confirmed the analysis. The [reproducer, results and kernel-path report](fskit-root-cache.md) document the evidence and a repeatable native runner.

Apple documents that [directory invalidation purges its name cache](https://developer.apple.com/documentation/fskit/fsvolume/kernelcachecoherencyaction/invalidate). File-data invalidation does not clear the parent's name mapping. A normal-adapter diagnostic build granting `.noCache` only to the root also failed root-level reopen. Remounting refreshes the view. A corrected root lookup must pass the standalone probe and both native snapshot contracts. Adding a stable account directory changes the mount layout and leaves top-level membership refresh unresolved; that alternative has not been implemented.

On 2026-11-02, a second standalone Swift probe verified actual file contents: after publishing B and attempting directory invalidation, reopening the cached root name returned A, reopening the child name returned B, and a fresh name in the same root immediately returned B. Old descriptors retained A, size and EOF throughout. File invalidation alone retained A on both paths. Revoking file items refreshed both paths only after old descriptors closed. The subsequent atomic-publication probe passed simultaneous A/B and A/B/C readers, including rename and removal. The [content experiment](fskit-root-cache.md#file-content-experiment-locating-the-stale-read) records the failing API controls separately from the working publication path.

## Open work in the same acceptance plan

1. Qualify real-provider revision changes and measure root metadata polling and Finder workloads. Root/nested native fixture acceptance is complete; the OS cache API reproducer remains an independent upstream test.
2. Linux and Windows native contracts passed. Complete additional native inode/FORGET and path replacement coverage.
3. Implement signed macOS peer reconnection and endpoint discovery after extension restart. The current one-use bootstrap does not recover a relaunched extension. Test transport, daemon, extension, and reauthentication recovery separately.
4. Finish explicit forced shutdown, retry after a failed native unmount, and bounded shutdown behavior. Current errors remain visible; a failed native unmount may still leave the OS mount present.
5. Complete accounting/admission for metadata in-flight allocations, object/node tables, reader tables, temporary files, and process overhead. Shared block payloads and request concurrency already have limits; some identity maps still grow with observed objects.
6. Measure the implemented demand-read/adaptive-prefetch policy against real provider latency and Finder/QuickLook workloads. Fetch size is now independent of the 4 MiB storage block; the controlled and native small-read results are recorded below.
7. Carry request IDs across the complete read path and expose bounded diagnostics. Add native workload measurements for Finder, QuickLook, media seeking, rate limiting, and TTL selection. Current TTL and whole-file limits are provisional.
8. Complete per-surface Unicode/case/name collision rules, remote replacement tests, PKU real revision mutation cases, and Baidu digest qualification.

The completed code and these open items belong to the same delivery. WebDAV and old-account migration remain at the end of the product backlog.

## Read delivery measurements

On 2026-11-02, the local macOS 27.2 native suite passed with the demand-range cache. `TestFSKitSmallReadFetchGranularity` opened an 8 MiB fixture through the installed FSKit extension: a 32 KiB application read generated exactly one provider range, offset 0 / length 32768. Its local fixture latency was 1.25 ms. Root/nested snapshots, publication, read-only operations and unmount also passed. This test uses local fixture bytes, not Baidu service throughput.

`BenchmarkBaiduReadDelivery` uses a local HTTP server that emits 32 KiB per millisecond. The baseline issues the former synchronous 4 MiB request. Three measured iterations on the same host produced:

| Workload | Delivery | Time/op | Remote bytes/op | Data GETs/op | dlink calls/op |
| --- | --- | ---: | ---: | ---: | ---: |
| First 32 KiB | 4 MiB fill baseline | 149.34 ms | 4,194,304 | 1 | 1 |
| First 32 KiB | Demand range | 2.34 ms | 32,768 | 1 | 1 |
| Sequential 4 MiB | 4 MiB fill baseline | 148.98 ms | 4,194,304 | 1 | 1 |
| Sequential 4 MiB | Demand + adaptive prefetch | 148.87 ms | 4,194,304 | 10 | 1 |

The first-read transfer falls by 128 times in this workload. Sequential transfer size stays constant; additional requests are the cost of early delivery and adaptive read-ahead. The fixture has local request latency, so real WAN round-trip costs require a separate measurement. Run it with:

```sh
go test ./internal/providers/baidu -run '^$' -bench BenchmarkBaiduReadDelivery -benchtime=3x -count=1
```

Deterministic regressions hold the tail of a prefetch HTTP response open and verify that foreground reads return its ready prefix. They also cover seek cancellation, concurrent download-link reuse and renewal, overlapping range reuse, failed partial responses, full-block assembly/restart, memory budgets, independent waiter cancellation and unchanged whole-object digest validation. The cancellation/sharing/provider cases additionally passed 20 repetitions with the race detector. No real account credentials are needed for these checks.

## Runtime minimums

Go 1.25 supplies confined `os.Root.Rename` and `os.Root.Chtimes`; see the [Go 1.25 release notes](https://go.dev/doc/go1.25). The app deployment target is macOS 26, where Path URL resources and OpenCloseOperations support the compatibility lifecycle. Xcode 26 builds that lifecycle; Xcode 27+ builds both lifecycles with runtime selection. macOS 27 supplies the FSKit [DataCacheHandler](https://developer.apple.com/documentation/fskit/fsvolume/datacachehandler) and [setCacheState](https://developer.apple.com/documentation/fskit/fsvolume/setcachestate(for:cachemode:coherencytype:action:)) APIs used by the extension.

## Existing accounts and persistence

Mounts requesting persistent retention require a saved AccountID. A legacy account without that field returns an explicit error before credential lookup; it can still be mounted with `--persistent-cache=false`. This work does not rewrite old account records or move their credential items. New saved bindings provide stable cache identity.
