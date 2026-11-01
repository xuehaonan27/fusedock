# FSKit root-directory cache invalidation on macOS 27.2

Observed on 2026-11-01, macOS 27.2 (26B123), arm64. The extension was built with the macOS 27 SDK, signed locally, enabled, and mounted successfully.

`FSVolume.setCacheState(for: root, cacheMode: .readWithCache, coherencyType: .readCache, action: .invalidate)` returns `NSOSStatusErrorDomain -536870206` (`0xe00002c2`, `kIOReturnBadArgument`). The same call on a looked-up child directory succeeds. The kernel logs:

```text
methodSetCacheState: no lifsnode found for fh
```

The failure occurs before the kernel applies the cache action. A standalone Swift volume reproduces it with two `FSItem` instances and no provider, account, broker, VFS, or block-cache calls. This isolates the failure to the FSKit/LIFS path on this OS build.

## User-visible impact

The ordinary FuseDock native contract changes a root-level file from one immutable revision to another, invalidates daemon metadata, and reopens the path. The reopened file continues to return the old 8,388,625-byte revision throughout the five-second observation window. The same contract inside a subdirectory passes: new opens see the new revision while old descriptors retain their bytes, size, and EOF. Same-size changes and remote rename also pass there. Remounting refreshes both root-level and nested content.

The full macOS 27 native acceptance result is **failed**. Successful builds and nested-file tests remain separate results.

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
| Unmount, refresh daemon metadata, mount | Refreshes the whole tree; verified locally | Ends the mount and requires applications to release their open files; it does not satisfy in-place refresh acceptance |
| Stable top-level account directories | Places provider files in ordinary child directories where the snapshot contract passes | Adds a path component; top-level account add/remove/rename still requires root invalidation or remount. This layout is not implemented by this investigation |

Apple documents [directory name-cache purging by `.invalidate`](https://developer.apple.com/documentation/fskit/fsvolume/kernelcachecoherencyaction/invalidate), [cache-state calls outside module locks](https://developer.apple.com/documentation/fskit/fsvolume/setcachestate(for:cachemode:coherencytype:action:)), and [`.revoke` as revoking access and reclaiming the vnode](https://developer.apple.com/documentation/fskit/fsvolume/kernelcachecoherencyaction/revoke). Revoking a live file conflicts with FuseDock's retained-descriptor contract. A new directory layout changes the filesystem interface and needs a separate design decision.

The remaining root issue is tracked in [the acceptance plan](../TODO.md#platform-isolation-and-native-tests). This report and reproducer are ready for an upstream issue; no report has been submitted to Apple.
