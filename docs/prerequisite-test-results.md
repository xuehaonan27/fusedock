# Preflight prerequisite results

| Date | Environment |
| --- | --- |
| 2026-10-26 | macOS 26.5.1, arm64, Go 1.27.3, FSKit extension installed from the current working tree. No Linux host, container runtime, or `/dev/fuse` was available. |

These are historical measurements used by [TODO.md](../TODO.md). The plan was revised on 2026-10-28; the deleted WebDAV implementation and its former tests are historical evidence only.

## Results

| Gate | Result | Evidence / remaining work |
| --- | --- | --- |
| FSKit persistent object IDs | Fixed to `false` | The rebuilt extension installed and a PKU volume mounted and listed successfully. The privilege was not required for basic read behavior. |
| FSKit extension restart | Failed as predicted | After killing the extension process, `stat` on a mounted child returned `ENOENT`. This records a mount failure, not an isolated proof of its only cause. Full daemon acceptance distinguishes authenticated reconnection, volume recovery, and existing-read recovery. |
| WebDAV recursive PROPFIND | Historical: implementation/tests deleted on 2026-10-28 | `Depth: 1` returned `207`; `Depth: infinity` returned `403` with `WebDAV depth infinity is not supported`. |
| Keychain prompt churn | Fixed for unchanged state | PKU previously forced a refresh and Keychain write on every open; Baidu rewrote state after reopening. Restore now uses the saved access token and refreshes on a real 401. Baidu skips a username write when unchanged. `SaveState` skips identical state. After rebuilding, real `fusedock ls --account baidu` and `--account pku` both completed without a security prompt. |
| Baidu MD5 fields | Only a subset is usable as MD5 | One metadata scan covered 823 files: 116 had canonical 32-character lowercase hexadecimal values; 707 had 32-character non-hexadecimal values. In the download sample, 5 canonical MD5 values matched downloaded bytes, repeat downloads, and a range comparison; 55 non-canonical fields were not MD5. This blocks default whole-file digest caching. |
| Folder listing latency | Measured roughly | Three root `ls` runs took 2.74–3.71 seconds for Baidu and 3.45–3.67 seconds for PKU, including process startup and login restore. These are not yet Finder relist measurements. |
| FSKit cold/warm read | Measured on PKU | The first 1 KiB read of a PDF took 8.84 seconds; the immediate second read took 0.000016 seconds. The VFS still expands a cold small read to a 4 MiB aligned provider range, so block size remains provisional. |
| Metadata indexing | Not proven | `mdutil -s` reported indexing disabled for the temporary test mount, which may reflect the `/tmp` location rather than the FSKit volume. Synthetic `.metadata_never_index` behavior still needs tests at a normal home-directory mount. |
| go-fuse library identity | Reproduced on macOS on 2026-10-28 | An equivalent temporary test called `fs.NewNodeFS`/raw Lookup with two distinct nodes sharing Mode/Ino/Gen. Both returned node ID 2 and inode 424242. No mount was involved. The existing Linux-tagged test still needs its own independent library target. |
| Linux native identity | Outstanding | Actual `st_ino`, readdir `d_ino`, lookup/getattr, and FORGET acceptance requires a Linux FUSE host. It is separate from the reproduced library behavior. |
| Windows native identity | Outstanding | Native WinFsp acceptance belongs to the independent Windows suite. |
| In-memory cap | Not implemented yet | Per-handle blocks remain unbounded. A synthetic thousand-handle test must be added with the memory-budget implementation. |

## What is now ruled out

- Treating every Baidu `md5` field as a valid MD5 digest. Only the canonical 32-hex subset is eligible, and even that is not yet sufficient for default caching without duplicate-content and concurrent-change tests.
- Making a small real-file read without accounting for 4 MiB range amplification.
- Considering a one-use capability file sufficient for FSKit process restart.

## Next tests

- Create duplicate-content test files through an owner-controlled test account (not the user's existing data), modify one copy, and verify listed MD5 changes and canonical fields remain trustworthy.
- Mount at a normal user home path, test a synthetic `.metadata_never_index`, and count provider calls during Finder/Spotlight/QuickLook activity.
- Add a Linux FUSE VM or host and test `st_ino`, readdir `d_ino`, lookup/getattr consistency, and remount behavior.
- Add provider/VFS counters for Finder relist, first-byte delay, network amplification, and resource budgets as part of the complete filesystem implementation.

## Second review closure

- Whole-object digest verification is accepted under five rules: provider-declared digest embedded in ContentVersion; compare with the OpenRead snapshot; whole-object only; mismatch returns `ErrConflict` and publishes nothing; digest reliability passes modification, duplicate-content, and concurrency tests.
- ObjectID/path cache identity and cache-failure isolation were review-aligned constraints, not rejected proposals.
- go-fuse library collision behavior has now been reproduced without a mount; Linux/macOS/Windows native integration remains separately measured.
- Startup budget/LRU metadata should come from stat size/mtime; header and payload verification move to first access.
