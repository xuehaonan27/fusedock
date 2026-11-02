# FSKit root cache failure and namespace publication on macOS 27.2

Observed on 2026-11-01 and 2026-11-02, macOS 27.2 (26B123), arm64. The extension was built with the macOS 27 SDK, signed locally, enabled, and mounted successfully.

`FSVolume.setCacheState(for: root, cacheMode: .readWithCache, coherencyType: .readCache, action: .invalidate)` returns `NSOSStatusErrorDomain -536870206` (`0xe00002c2`, `kIOReturnBadArgument`). The same call on a looked-up child directory succeeds. The kernel logs:

```text
methodSetCacheState: no lifsnode found for fh
```

The failure occurs before the kernel applies the cache action. A standalone Swift volume reproduces it with two `FSItem` instances and no provider, account, broker, VFS, or block-cache calls. This isolates the failure to the FSKit/LIFS path on this OS build.

## User-visible impact

Before the publication fix, the ordinary FuseDock native contract changed a root-level file from one immutable revision to another, invalidated daemon metadata, and reopened the path. The reopened file continued to return the old 8,388,625-byte revision throughout the five-second observation window. The same contract inside a subdirectory passed.

The signed normal build at `566631d` passes **both root and nested native snapshot contracts** on this host. It publishes root namespace changes with native rename/unlink instead of calling the failing root cache API. The independently reproduced OS API failure remains documented below.

## Implemented resolution

The extension maintains the root namespace's published metadata. At `EntryTTL`, it compares the current broker listing with that view. A changed file gets a new FSItem under a unique temporary name; the host performs an actual rename over the old name. The rename callback commits the new mapping, and LIFS purges the old name-cache entry. Existing descriptors retain the old FSItem and read handle. Remote renames and removals use native rename and unlink/rmdir respectively. Subdirectories continue to use the successful FSKit directory-invalidation API.

The initial atomic-replacement probe passed old-A/new-B reads, including size and previously unread data. Extended testing with unique staging names passed A→B→C, a same-size B→C update, rename into a negatively cached name, and deletion with all three revisions retained. Reusing a staging name failed on the second publication because that staging name was negatively cached after the first rename; every publication now uses a fresh name.

The normal adapter passed the original, unchanged root snapshot acceptance. Additional native tests passed file and directory creation, negative-cache refresh, remote rename, same-size local-provider replacement, removal with an open old reader, directory-to-file replacement, and enumeration without internal names. User create, mkdir, rename, unlink and a forged publication-name rename were rejected.

### Authorization and filesystem behavior

- The broker advertises publication capability only when the host binds it to a resource. The RPC uses the existing authenticated session and browser-origin rejection. Requests contain immediate child names, never a caller-selected mount path.
- The Darwin host locates a `fusedock` mount owned by its UID with the exact bound resource URL, opens that root without following a symlink, verifies its filesystem identity, and uses `renameat`/`unlinkat` relative to the descriptor. The descriptor closes after the operation.
- The extension accepts only the pending operation's names and FSItem, expected overwritten revision, root directories and mounting UID. A successful callback consumes that authorization. Other mutations return a read-only error. No provider write operation was added.
- Staging names are unique and excluded from enumeration. A staging name refers to the actual staged revision; rename really changes the local mapping. Removal really removes its entry. The provider credentials remain in the host.
- Root directory mode is `0755` to permit native publication to reach the module; files remain `0444`. Write permission bits on the root do not grant arbitrary namespace mutations. This distinction is covered by the native mutation-rejection tests.
- Root refresh now incurs a metadata list at each TTL and a native operation per changed entry. It retains a root metadata view and processes the diff by object identity. It preserves normal file caching, the mount path, and old read handles.
- A current broker is required. Managed CLI/daemon mounts and `broker start` bind a publisher; legacy direct-provider resource files do not have that host channel.

Root lookup and reclaim share a synchronization context and use Apple's [tryReclaim API](https://developer.apple.com/documentation/fskit/fsitem/tryreclaim(_:)) so a racing lookup can retain an item the kernel tried to reclaim. Publication syscalls run outside module locks, allowing kernel callbacks to reenter the extension.

## Independent reproduction

Source: [RootCacheProbe.swift](../macos/Tests/RootCacheProbe.swift). It implements a read-only volume with a root and one empty directory named `child`. Activation returns the root instance; lookup returns the child instance. Both expose complete directory attributes. After mounting, a timer calls the four non-revoking cache actions on each instance, outside module locks.

| Variation | Root result | Child result |
| --- | --- | --- |
| Root created first | `-536870206` | Success |
| Child created first | `-536870206` | Success |
| Root grants `.noCache` on open and upgrade | `-536870206` | Success |
| Root file ID changed from 2 to 42 | `-536870206` | Success |
| Root parent ID changed from 1 to its own ID | `-536870206` | Success |
| Path URL resource is a file or a directory | `-536870206` | Success |
| `.push`, `.pushInvalidate`, `.invalidate`, `.update` | `-536870206` for each | Success for each |

The committed runner was exercised against [the diagnostic build of `6a28cdd`](https://github.com/xuehaonan27/fusedock/actions/runs/41315622257): five configurations × two resource types, with eight action results per mount. All 40 root calls returned `-536870206`; all 40 child calls succeeded. The kernel log contained 40 matching `no lifsnode found for fh` messages. The runner returned status 1 and unmounted all ten fixtures. After restoring the normal signed app, `make test-macos` again passed remount refresh and the nested snapshot contract and failed root-level reopen.

A separate build of the ordinary adapter granted `.noCache` only for the root. Its full native contract still failed at root-level reopen and passed in the nested case. The five cache-mode/coherency combinations from the earlier adapter probe also failed only for the root. These diagnostic variants are excluded from normal app builds.

## File-content experiment: locating the stale read

The 2026-11-02 investigation added [ReadCacheProbe.swift](../macos/Tests/ReadCacheProbe.swift) and [its native runner](../scripts/probe-macos-read-cache.py). This standalone Swift volume exposes `file.txt` in the root and in `child/`. Revision 1 contains A bytes and is 8,388,625 bytes long; revision 2 contains B bytes and is 8,388,640 bytes long. Each FSItem retains its revision. A private external state file selects the current revision and the cache operation. The runner waits for the module to finish that operation before reopening the paths.

The runner keeps both original descriptors open, publishes revision 2, and compares ordinary reopen, an uncached name (`fresh.txt`) resolving to the same current item, the retained descriptors, and reopen after closing the original descriptors. Prefixes, an initially unread range at 8 MiB, size and EOF are checked. The retained descriptors are checked again after opening the fresh name.

| Operation after publishing B | Reopen root `file.txt` while old FD is open | Reopen `child/file.txt` while old FD is open | Fresh name in either directory | Old descriptors | Reopen after closing old descriptors (root / child) |
| --- | --- | --- | --- | --- | --- |
| No invalidation (control) | A | A | B | A | A / A |
| Invalidate both parent directories | A | B | B | A | A / B |
| Invalidate both old file items | A | A | B | A | A / A |
| Revoke both old file items | A | A | B | A | B / B |

All rows were observed using [the signed diagnostic build from `0208ebc`](https://github.com/xuehaonan27/fusedock/actions/runs/41331978546). Directory invalidation returned `-536870206` for the root and zero for the child. File invalidation and file revocation returned zero for both files. No row satisfies simultaneous old-A/new-B opens in both locations; the runner exits 1 and records each case separately.

In the directory-invalidation case, callback logs show this order:

```text
event=invalidate parent=root revision=0 action=directory code=-536870206
event=invalidate parent=child revision=0 action=directory code=0
event=published revision=2 action=directory
event=lookup parent=child revision=2 name=file.txt
event=lookup parent=root revision=2 name=fresh.txt
```

Reopening root `file.txt` returns A without a new lookup for that name. Looking up root `fresh.txt` reaches revision 2 and reads B while the old descriptor still reads A. This demonstrates that the current revision and read delivery work within the same root, while the previously cached name keeps resolving to the old item. The child-directory result demonstrates the effect of a successful parent-directory invalidation.

The normal adapter was also built with tracing at `item(for:)` and open/upgrade, using the `native-trace` variant ([build](https://github.com/xuehaonan27/fusedock/actions/runs/41331564427)). The root native contract recorded two initial file-item resolutions of `v1` (this helper is used by both lookup and enumeration), one file open/upgrade of `v1`, and no resolution of `v2` during the five-second stale-read window. Root invalidation failed 100 times during that window. The nested contract resolved and opened `v2` and `v3` and passed. These normal-adapter traces connect the standalone name-cache behavior to the actual FuseDock failure.

The `.revoke` result narrows the previous documentation-based assessment: on this build it does **not** immediately break the retained descriptors. It permits fresh resolution after the old descriptors close. It does **not** provide fresh opens while those descriptors remain open, which is the required contract. The ordinary adapter continues to use directory invalidation; this experiment adds no revocation policy to it.

To repeat the content experiment, follow the signing/restore procedure below with workflow variant `read-content`, then run:

```sh
python3 scripts/probe-macos-read-cache.py --output /tmp/fusedock-content-evidence
```

The output directory must be new. `result.json` contains all byte/size/EOF observations, and `probe.log` contains the corresponding lookup, open, read, close, reclaim and cache-operation events. `--action directory` selects the shortest stale-root/fresh-child comparison. The probe uses the legacy FSVolume operation callbacks plus DataCacheHandler; the normal-adapter trace uses the current Handler/ReadWriteHandler callbacks.

### Run the reproduction

The manual [fskit-root-probe workflow](../.github/workflows/fskit-root-probe.yml) builds the diagnostic app with Xcode 27. Select `standalone`; the `root-no-cache` variant instead builds the ordinary adapter with only the root's initial coherency changed.

1. Unmount FuseDock volumes. Archive the current signed app with `ditto -c -k --sequesterRsrc --keepParent "$HOME/Applications/FuseDock.app" /tmp/FuseDock-normal.zip`.
2. Dispatch `gh workflow run fskit-root-probe.yml -f variant=standalone`. Download `FuseDock-root-probe-standalone` from the completed run.
3. Sign the downloaded app using the existing local identity, entitlements and FSKit provisioning profile as described in [the signing instructions](macos-fskit.md#ci-builds-when-local-xcode-is-older). Install it with `scripts/install-macos-fskit.sh <signed-app-path>`. The probe uses the existing app and extension identifiers.
4. Run `python3 scripts/probe-macos-root-cache.py`. The runner creates private fixtures, mounts each case, looks up the child, collects all eight root/child action results, and unmounts. It tests both file and directory resources. It prints the evidence directory containing `probe.log` and `result.json`. Each mount has a unique log identifier. Exit status 1 records failed cache calls or missing samples; success requires all calls to succeed.
5. To run one case: `python3 scripts/probe-macos-root-cache.py --case root-first --resource-kind file`. This is the shortest reproducer.
6. Restore the archived normal app with `ditto -x -k /tmp/FuseDock-normal.zip /tmp/FuseDock-restore` followed by `scripts/install-macos-fskit.sh /tmp/FuseDock-restore/FuseDock.app`. Unregister and remove extracted test app copies after restoration.
7. Run `make test-macos` against the restored app. The root and nested contracts report independently.

The probe runner collects only its own log subsystem. The kernel message can be collected separately immediately after a run:

```sh
/usr/bin/log show --last 2m --style compact \
  --predicate 'process == "kernel" AND eventMessage CONTAINS "methodSetCacheState"'
```

## Kernel path analysis

Read-only inspection of the local LIFS binary explains the observed root/child difference:

1. `_lifs_mount` allocates the root node and creates its vnode directly. It stores the root vnode on the mount. The ordinary-node hash table is initialized afterward; this path does not insert the root into that table.
2. `_lifs_root` obtains the root vnode directly from the mount. Ordinary traversal can therefore use the root successfully.
3. `_lifs_create_node_ext` inserts ordinary nodes into the mount's hash table, keyed by the mount index and file handle.
4. `AppleLIFSUserClient::methodSetCacheState` resolves its target through `_lifs_lookup_node`. That lookup searches the hash table and has no root-vnode special case. When lookup returns no node, it logs the message above and returns `kIOReturnBadArgument`.
5. For a found node, `_lifs_apply_cache_action` clears file data/attributes for regular files; for directories it also calls `cache_purge`. Invalidating a regular file therefore does not replace invalidating its parent directory's name cache.

This is our analysis of build 26B123, supported by the independent runtime reproduction. Apple has not confirmed it. Raw system binaries and disassembly are kept outside the repository. The installed kernel and frameworks were unchanged.

Changing inode numbers, creation order or cache-action arguments cannot repair the missing lookup in this path. Public `FSItem` instances do not expose a method to insert their kernel root vnode into the ordinary-node table.

## Resolution and filesystem tradeoffs

The required behavior remains: the existing mount layout, new opens resolving a refreshed revision, and old open descriptors retaining their selected revision. The root and nested native contracts must both pass against the same normal signed build.

| Approach | Effect | Cost or remaining limitation |
| --- | --- | --- |
| OS correction to root cache-state lookup | Makes the documented directory invalidation path usable for the root | Requires a corrected system build and rerunning both the standalone and native contracts |
| Implemented native namespace publication | Fresh root opens and retained old descriptors pass together | Root metadata polling, one native operation per change, internal staging and an authenticated host publisher |
| Unmount, refresh daemon metadata, mount | Refreshes the whole tree; verified locally | Ends the mount and requires applications to release their open files; it does not satisfy in-place refresh acceptance |
| Stable top-level account directories | Places provider files in ordinary child directories where the snapshot contract passes | Adds a path component; top-level account add/remove/rename still requires root invalidation or remount. This layout is not implemented by this investigation |

Apple documents [directory name-cache purging by `.invalidate`](https://developer.apple.com/documentation/fskit/fsvolume/kernelcachecoherencyaction/invalidate), [cache-state calls outside module locks](https://developer.apple.com/documentation/fskit/fsvolume/setcachestate(for:cachemode:coherencytype:action:)), and [`.revoke` as revoking access and reclaiming the vnode](https://developer.apple.com/documentation/fskit/fsvolume/kernelcachecoherencyaction/revoke). The measured revocation behavior above fails simultaneous old/new-revision acceptance while allowing refresh after old descriptors close. A new directory layout changes the filesystem interface and needs a separate design decision.

The application-level root refresh fix is recorded in [the acceptance plan](../TODO.md#platform-isolation-and-native-tests). The standalone API reproduction remains suitable for an upstream issue; no report has been submitted to Apple.
