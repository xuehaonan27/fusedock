# Persistent block cache

Status: design proposal. The current implementation has only a short-lived in-memory single-block cache per open handle. No file data is currently persisted by FuseDock.

## What it is for

The persistent block cache should let a later FSKit, WebDAV, or CLI operation reuse a range that an earlier operation already downloaded. It is a read cache, not a sync engine, and it should survive daemon restarts without becoming a second source of truth.

Expected wins:

- Reopen a PDF, video, or document without downloading the same 4 MiB ranges again.
- Share warm ranges between FSKit and WebDAV in the same broker process.
- Avoid repeated signed-URL authorization and range GETs for PKU and Baidu.
- Bound disk use with deterministic eviction instead of temporary whole-file downloads.
- Keep cold metadata responsive while hot file data is read locally.

The cache must not:

- turn a stale path name into an object identity;
- merge local and remote contents;
- hide a provider that returned incomplete or out-of-range bytes;
- cache credentials or signed URLs;
- silently serve a newer file under an old ETag/version;
- fall back to an unverified whole-file download when a provider ignores Range.

## Current gaps

The present internal/vfs reader keeps one 4 MiB block per open handle. Closing the handle loses it, and each mount surface constructs its own VFS wrapper. Meanwhile the broker data protocol currently calls the provider session directly. A shared persistent layer therefore needs one intentional owner, not another wrapper around an already wrapped reader.

The target process shape is:

~~~
                         one broker/daemon process
account store/keychain -> provider.Session -> vfs.Session -> block cache
                                               |
                                  +------------+------------+
                                  |                         |
                            FSKit broker endpoint      WebDAV endpoint
~~~

For direct Linux FUSE mounts before fusedockd exists, the mount process may own the same cache through the same exclusive lock. It must not share the directory with a running daemon.

## Cache identity

A block key has these logical parts:

~~~
account ID + provider ID + endpoint + object ID + object version
           + file size + block offset/length
~~~

The account ID must be a new immutable value in the account store, not the display name. Renaming an account must not rename its cache namespace. Recreating an account with the same name must receive a new ID so old file bytes cannot be mistaken for the new account's data.

The path name and cache header must not contain a readable account name, remote file name, object path, signed URL, or token. Use a SHA-256 digest of a length-prefixed encoding:

~~~
keyHash = SHA-256(
  "fusedock-block-v1" || NUL ||
  len(accountID) || accountID ||
  len(providerID) || providerID ||
  len(normalizedEndpoint) || normalizedEndpoint ||
  len(objectID) || objectID ||
  len(version) || version ||
  decimal(fileSize) || NUL ||
  decimal(blockOffset) || NUL ||
  decimal(blockLength)
)
~~~

Length prefixes avoid ambiguity when an object ID contains delimiters. The endpoint is included so two servers cannot share blocks because of an account-store copy or migration mistake.

### Version requirements

A disk hit is allowed only when the caller already has an Object with the same version used in the key. A changed version chooses a different hash; old blocks become unreachable and are evicted by normal LRU.

This does not make stale metadata disappear. The metadata TTL still controls how long the system may report an older Object. For read-only mounts, serving a previously selected revision during that bounded TTL is acceptable. If stronger freshness is required, the mount should revalidate the file with Stat before opening it after the TTL expires.

More importantly, OpenRead(object) must return the revision described by object.Version. A provider that ignores the supplied object and downloads the current remote file can poison a cache entry. PKU already obtains a revision-bound signed request. Baidu's current dlink path does not yet prove that the returned bytes are tied to the metadata version, so Baidu persistent caching must remain disabled until it performs a version check before the fill, or binds the download to a revision.

If Version is empty, persistent caching is disabled for that object by default. An optional local-only fallback may use a metadata fingerprint, but it must be explicitly configured and documented as weaker.

## Storage layout

Default roots:

| Platform | Root |
| --- | --- |
| macOS | ~/Library/Caches/fusedock/blocks/v1 |
| Linux | $XDG_CACHE_HOME/fusedock/blocks/v1, or ~/.cache/fusedock/blocks/v1 |
| Windows | %LOCALAPPDATA%\fusedock\cache\blocks\v1 |

~~~
blocks/v1/
  owner.lock
  tmp/
  entries/<keyHash>.blk
~~~

The directory is private (0700 on Unix). Block files are regular files created with 0600, inside an opened root that rejects symlinks and path traversal. Windows requires a protected current-user ACL and reparse-point rejection before this feature is enabled.

Each block file is self-describing and contains no remote names:

~~~
magic            4 bytes   "FDBK"
format version   2 bytes   uint16, currently 1
header length    2 bytes   uint16, currently 80
payload length   8 bytes   uint64
key hash        32 bytes   SHA-256 of the complete cache key
payload hash    32 bytes   SHA-256 of payload bytes
payload         N bytes
~~~

A file name is the hexadecimal key hash plus .blk. The in-memory index is rebuilt by scanning entries at daemon startup. A cache size of 10 GiB with 4 MiB blocks is roughly 2,560 full-size files, so a scan is practical and avoids adding a database dependency for the first implementation. If the cache later contains millions of tiny entries, a pure-Go transactional index such as bbolt may replace the scan.

## Read plan

Defaults for the first implementation:

| Option | Default | Meaning |
| --- | --- | --- |
| block-size | 4 MiB | Alignment for large-file ranges |
| whole-file | 16 MiB | Cache a complete file when its size is at or below this threshold |
| read-ahead | one block | No speculative multi-block fetch initially |
| fill-concurrency | 4 | Maximum simultaneous provider block fills |
| cache-limit | 10 GiB | Disk budget for cached payloads |
| metadata-ttl | 2 s | Existing metadata freshness window |

For a file at or below whole-file, the first read can fill the whole file as one cache entry. For a larger file, reads are aligned to block-size; the final block is shortened to the remaining file length. A read spanning two blocks is served in order in the first implementation. Parallel block prefetch is a measured optimization, not a correctness requirement.

On every lookup, check the whole-file key first, then the aligned block key. If a complete file would exceed the disk budget or whole-file caching is disabled, fall back to aligned blocks. Never allocate a whole-file entry merely because a small read happened to touch a large file.

The per-handle single-block buffer should become unnecessary once the persistent store is present. Let the operating-system page cache handle repeated reads from block files initially; add a small bounded memory L1 only if benchmark data shows that file opens are significant.

## Hit and fill flow

~~~
driver ReadAt
   -> validate offset/length against Object.Size
   -> compute block key
   -> lookup in-memory index
       hit: open/read local block, verify once, return bytes
       miss: join or create one in-flight fill
   -> fill full aligned range from provider
   -> validate exact byte count and file size
   -> write temp file, fsync, rename into entries/
   -> fsync directory
   -> return requested bytes
~~~

Only one fill may exist per block key. The fill owns its context rather than borrowing the first caller's request context. Each waiter can cancel independently; when the last waiter leaves, an unused fill may be cancelled. A successful fill may still finish and publish if another waiter remains. Provider fills are limited globally so a directory of parallel readers cannot exhaust sockets or memory.

The fill result is published only after the exact expected size is received. A short read, extra bytes, mismatched size, or provider cancellation removes the temporary file and returns an error; it never leaves a partial file under a valid cache name.

If the cache cannot write because of permission, disk pressure, or a full budget with every block pinned, a successfully downloaded block is returned to the caller and simply not retained. A cache write failure must not fail an otherwise valid read.

## Index, LRU, and eviction

The owner process keeps an in-memory entry index:

~~~
keyHash -> path, payload length, file size, lastAccess, refCount, verified
~~~

- lastAccess is updated under a short index lock; it is not a network or disk-I/O lock.
- Access time is flushed to file metadata periodically, not on every 32 KiB driver read.
- The total budget includes headers and payloads. Temporary files are tracked separately and limited by the same fill concurrency.
- Eviction removes the oldest clean, unpinned entries until the new block fits.
- An entry with live readers is pinned. On Unix, unlinking an open file may be safe; on Windows, deletion is deferred until the last handle closes.
- Version changes create new keys. Old-version entries are not eagerly deleted; LRU eventually removes them.
- Startup removes stale temporary files and deletes malformed or size-mismatched entries. A complete file is verified against its payload hash once per process before its first hit.
- On corruption, delete the entry and perform a normal provider fill in the same request. Never return bytes from a corrupt cache file.

If a block is larger than the configured budget, bypass persistence. A single account's activity must not be able to evict every block from another active account indefinitely; a later fairness policy can reserve or weight a share per account. The first version should expose per-account usage so this can be evaluated with real workloads.

## Concurrency rules

- Metadata cache lock: short; never held across provider or disk I/O.
- Block index lock: short; protects map, LRU links, refcounts, and budget accounting.
- Per-block flight: serializes one remote fill without serializing unrelated blocks.
- Per-file handle: serializes its local offset state only; ReadAt remains safe for concurrent use.
- Cache owner: exactly one process owns a cache root through owner.lock. A second process receives a clear error or runs without persistent caching; it must not race eviction in the same directory.
- Unmount/daemon shutdown: cancel unused fills, wait for active I/O, flush access metadata, and leave completed entry files intact for the next run.

## Configuration and controls

Proposed commands:

~~~sh
fusedock cache info
fusedock cache clear --account baidu
fusedock cache verify
fusedock cache prune --older-than 30d
~~~

Proposed service options:

~~~
--cache-dir PATH
--cache-limit SIZE
--cache-block-size SIZE
--cache-whole-file SIZE
--cache-read-ahead SIZE
--cache-fill-concurrency N
--no-data-cache
~~~

cache info should report logical bytes, physical bytes, hit/miss counters, coalesced waiters, evicted bytes, in-flight fills, corrupt entries, write failures, and the root path. It must not list object names by default.

## Security and privacy

File payloads can be highly sensitive even though credentials are not stored here.

- Cache root is private; files and temporary writes are owner-only.
- Object names and IDs never appear in paths or filenames.
- Remote names remain only in the in-memory metadata cache, not the disk data index.
- The cache contains plaintext payloads by default. Users who need encrypted-at-rest data should use full-disk encryption; optional per-cache encryption is a later feature.
- cache clear removes references and files, but cannot guarantee physical erasure on SSDs or backed-up filesystems.
- A symlink, hard-link, reparse point, or changed file size in an entry path is treated as corruption; the entry is removed.
- No signed URL, token, password, OAuth code, or HTTP header is stored in a block file.
- Cache-disk exhaustion is a cache miss, not a successful persistence guarantee and not a reason to corrupt an application read.

## Observability

Add counters for:

- hit/miss bytes and requests;
- whole-file versus aligned-block fills;
- provider bytes fetched once and reused;
- waiters joined per fill;
- fill cancellations and fetch-limit waits;
- evictions and bytes reclaimed;
- corrupt headers/payloads and cache write errors;
- temporary-file cleanup;
- cache budget and per-account usage.

All cache logs should use key hashes and sizes, not remote file names, object IDs, or signed URLs. A diagnostics export may include aggregate counters without user content.

## Testing and acceptance

Required unit and integration coverage:

- cold fill, warm hit, and hit after a simulated process restart;
- object version change gives a different key and never returns old bytes;
- account ID change or recreated account isolates cache namespaces;
- empty version bypasses persistence;
- reads spanning block boundaries and the final short block;
- EOF boundaries and invalid lengths;
- multiple waiters coalesce into one provider fill;
- one waiter cancelling does not abort a useful shared fill;
- all waiters cancelling cancels unused work;
- fill concurrency limit, provider short reads, oversized responses, and context cancellation;
- interrupted temporary writes and startup orphan cleanup;
- corrupt header, corrupt payload, wrong file length, symlink, and permission errors;
- LRU eviction, pinned readers, Windows deferred deletion, and disk-full behavior;
- FSKit and WebDAV reading the same warm block without a second provider call;
- exact byte comparison against a provider that honors Range but not a whole-file fallback.

Acceptance for enabling it by default:

1. A 10 GiB cache remains under its configured budget during mixed small/large reads.
2. The same unchanged file reopened after daemon restart produces zero provider data calls for cached ranges.
3. A changed provider version produces zero bytes from the previous version.
4. FSKit and WebDAV share one warm fill in the same broker process.
5. Cache corruption is detected and repaired by a remote refill.
6. Cold and warm path benchmarks show a measurable improvement without unacceptable memory growth or lock contention.

## Rollout sequence

1. Add stable account IDs and version-bound provider conformance tests.
2. Add the cache package with an in-memory index and no integration; exercise it against local fixtures only.
3. Publish and validate block files atomically; add restart, corruption, and eviction tests.
4. Wire one cached VFS session into both FSKit and WebDAV in the broker process.
5. Disable duplicate handle-block caches in the extension where the shared broker cache already covers the data.
6. Add cache diagnostics and an opt-in configuration.
7. Run native PKU and Baidu read acceptance, with Baidu persistence disabled until revision binding is fixed.
8. Enable a conservative default after cold/warm and soak measurements.

## Explicit first-version limits

- Read-only data only; no write staging or pending-write eviction policy yet.
- No cross-process cache sharing beyond the single-owner daemon.
- No cross-account content deduplication.
- No offline synchronization, conflict merge, or remote invalidation push.
- No per-block encryption or user-supplied cache key in the first release.
- No provider that lacks a trustworthy version should silently use the disk cache.
- The local sample provider should bypass the persistent data cache by default: its bytes already live on local storage and its current open handle is not a remote snapshot.
