# Preflight prerequisite results

| Date | Environment |
| --- | --- |
| 2026-10-26 | macOS 26.5.1, arm64, Go 1.27.3, FSKit extension installed from the current working tree. No Linux host, container runtime, or `/dev/fuse` was available. |

These are preflight measurements for the review gates in [TODO.md](../TODO.md). They are not a claim that the persistent cache is ready for default enablement.

## Results

| Gate | Result | Evidence / remaining work |
| --- | --- | --- |
| FSKit persistent object IDs | Fixed to `false` | The rebuilt extension installed and a PKU volume mounted and listed successfully. The privilege was not required for basic read behavior. |
| FSKit extension restart | Failed as predicted | After killing the extension process, `stat` on a mounted child returned `ENOENT`; the extension did not reconnect. The one-use capability is not a restart-safe lifecycle. UDS/XPC work remains mandatory. |
| WebDAV recursive PROPFIND | Guard added and verified | `Depth: 1` returned `207`; `Depth: infinity` returned `403` with `WebDAV depth infinity is not supported`. |
| Keychain prompt churn | Fixed for unchanged state | PKU previously forced a refresh and Keychain write on every open; Baidu rewrote state after reopening. Restore now uses the saved access token and refreshes on a real 401. Baidu skips a username write when unchanged. `SaveState` skips identical state. After rebuilding, real `fusedock ls --account baidu` and `--account pku` both completed without a security prompt. |
| Baidu MD5 fields | Only a subset is usable as MD5 | One metadata scan covered 823 files: 116 had canonical 32-character lowercase hexadecimal values; 707 had 32-character non-hexadecimal values. In the download sample, 5 canonical MD5 values matched downloaded bytes, repeat downloads, and a range comparison; 55 non-canonical fields were not MD5. This blocks default whole-file digest caching. |
| Folder listing latency | Measured roughly | Three root `ls` runs took 2.74–3.71 seconds for Baidu and 3.45–3.67 seconds for PKU, including process startup and login restore. These are not yet Finder relist measurements. |
| FSKit cold/warm read | Measured on PKU | The first 1 KiB read of a PDF took 8.84 seconds; the immediate second read took 0.000016 seconds. The VFS still expands a cold small read to a 4 MiB aligned provider range, so block size remains provisional. |
| Metadata indexing | Not proven | `mdutil -s` reported indexing disabled for the temporary test mount, which may reflect the `/tmp` location rather than the FSKit volume. Synthetic `.metadata_never_index` behavior still needs tests at a normal home-directory mount. |
| Linux FUSE identity | Blocked by host | There is no `/dev/fuse`, `fusermount3`, container runtime, or Linux VM on this machine. Builds and unit tests pass, but FUSE nodeid/`st_ino` behavior cannot be confirmed here. |
| In-memory cap | Not implemented yet | Per-handle blocks remain unbounded. A synthetic thousand-handle test must be added with the memory-budget implementation. |

## What is now ruled out

- Treating every Baidu `md5` field as a valid MD5 digest. Only the canonical 32-hex subset is eligible, and even that is not yet sufficient for default caching without duplicate-content and concurrent-change tests.
- Making a small real-file read without accounting for 4 MiB range amplification.
- Considering a one-use capability file sufficient for FSKit process restart.

## Next tests

- Create duplicate-content test files through an owner-controlled test account (not the user's existing data), modify one copy, and verify listed MD5 changes and canonical fields remain trustworthy.
- Mount at a normal user home path, test a synthetic `.metadata_never_index`, and count provider calls during Finder/Spotlight/QuickLook activity.
- Add a Linux FUSE VM or host and test `st_ino`, readdir `d_ino`, lookup/getattr consistency, and remount behavior.
- Add request counters to the daemon path before implementing persistent caching, so Finder relist and cache-hit costs can be measured without guessing.
