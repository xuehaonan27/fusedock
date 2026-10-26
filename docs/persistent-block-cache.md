# Persistent block cache

Status: design proposal. The current implementation has only a short-lived in-memory single-block cache per open handle. No file data is currently persisted by FuseDock.

The account, object, path, and inode identities used here are defined by [namespace-contract.md](namespace-contract.md). This design depends on that contract being implemented first.

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
account ID + provider ID + object ID + content version
           + file size + block offset/length
~~~

The account ID is an immutable value in the account store, not the display name. Renaming an account does not change its cache namespace. Recreating an account with the same name receives a new ID so old file bytes cannot be mistaken for the new account's data. Changing the provider, endpoint, username, or identity-affecting options also rotates the account ID.

The path name and cache header must not contain a readable account name, remote file name, object path, signed URL, or token. Use a SHA-256 digest of a length-prefixed encoding:

~~~
namespaceTag = SHA-256(
  "fusedock-namespace-v1" || NUL ||
  len(accountID) || accountID ||
  len(providerID) || providerID
)

keyHash = SHA-256(
  "fusedock-block-v2" || NUL ||
  len(accountID) || accountID ||
  len(providerID) || providerID ||
  len(objectID) || objectID ||
  len(contentVersion) || contentVersion ||
  uint64(fileSize) || uint64(blockOffset) || uint64(blockLength)
)
~~~

Length prefixes avoid ambiguity when an object ID contains delimiters. Integers are fixed-width big-endian values. The endpoint is a binding input that causes account-ID rotation, so it is not a second identity maintained by the cache. A bug that preserves an account ID across a rebind violates the namespace contract and must disable access to the old cache namespace rather than attempt to merge it.

### Version requirements

A disk hit is allowed only when the caller already has an ObjectRevision with the same ContentVersion used in the key. A changed version chooses a different hash; old blocks become unreachable and are evicted by normal LRU. Equal ContentVersion is meaningful only within one account/provider/object ID: two objects may legitimately share a token, but the full key keeps their bytes separate.

This does not make stale metadata disappear. The metadata TTL still controls how long the system may report an older Object. For read-only mounts, serving a previously selected revision during that bounded TTL is acceptable. If stronger freshness is required, the mount should revalidate the file with Stat before opening it after the TTL expires.

More importantly, **every remote request** must remain bound to the requested ContentVersion, not only the initial OpenRead. A reader can issue several range GETs, renew signed URLs, follow redirects, and retry; all of them must use the same revision and validate the response. For conditional GET, use `If-Match` with a strong validator, not `If-Range` as the version gate. Validate status 206, `Content-Range`, total size, and response ETag on every response. A 200 full response, ignored condition, mismatched ETag, or disappeared revision becomes `ErrConflict`.

PKU already signs `osdownload` with the requested `rev`, but its renewal and online-edit paths need tests. Baidu's current dlink path does not yet prove that returned bytes are tied to the metadata version, so Baidu range caching must remain disabled. If XPAN MD5 proves reliable, a file no larger than the whole-file threshold may be downloaded completely, hashed, and cached only when that digest matches metadata; this exception must not be generalized to ranges or larger files. MD5 is an accidental-change/compatibility check, not a security guarantee.

If ContentVersion is empty or the provider lacks per-request revision binding, persistent caching is disabled for that object by default. There is no metadata-fingerprint fallback in the persistent data cache; a weak fingerprint may only drive short-lived metadata caching.

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
  entries/<namespaceTag>/<keyHash[:2]>/<keyHash>.blk
~~~

The namespace-tagged directory is derived only from opaque IDs, allowing per-account accounting and clearing without exposing an alias or remote name. The root is private (0700 on Unix). Block files are regular files created with 0600, inside an opened root that rejects symlinks and path traversal. Windows requires a protected current-user ACL and reparse-point rejection before this feature is enabled.

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

A file name is the hexadecimal key hash plus .blk. The filesystem directory is the initial index; no bbolt database is required. Startup scans headers and file sizes in the background to establish budget and LRU order. Until that scan completes, new persistent writes are blocked or treated as an already-full budget. Payload hashes are verified on first access in each process and cached in-memory as verified, rather than hashing every small read.

## Read plan

Defaults for the first implementation:

| Option | Default | Meaning |
| --- | --- | --- |
| block-size | 4 MiB | Alignment for large-file ranges |
| whole-file | 16 MiB | Cache a complete file when its size is at or below this threshold |
| read-ahead | one block | No speculative multi-block fetch initially |
| fill-concurrency | 4 | Maximum simultaneous provider block fills |
| cache-limit | 10 GiB | Disk budget for cached payloads |
| metadata-ttl | measured per provider | Remote providers should not inherit the current fixed 2 s default; establish the value from Finder relist and provider rate-limit measurements |

The block, whole-file, read-ahead, and TTL values are provisional until baseline measurements cover PKU/Baidu latency, Finder relist calls, QuickLook behavior, and range throughput.

For a file at or below whole-file, the first read can fill the whole file as one cache entry. For a larger file, reads are aligned to block-size; the final block is shortened to the remaining file length. A read spanning two blocks is served in order in the first implementation. Parallel block prefetch is a measured optimization, not a correctness requirement.

On every lookup, check the whole-file key first, then the aligned block key. If a complete file would exceed the disk budget or whole-file caching is disabled, fall back to aligned blocks. Never allocate a whole-file entry merely because a small read happened to touch a large file.

The per-handle single-block buffer should become unnecessary once the persistent store is present. Let the operating-system page cache handle repeated reads from block files initially; add a small bounded memory L1 only if benchmark data shows that file opens are significant.

## Hit and fill flow

~~~
driver ReadAt
   -> validate offset/length against Object.Size
   -> compute block key
   -> lookup in-memory index
       hit: open/read local block; verify payload once per process, then return bytes
       miss: join or create one in-flight fill
   -> fill full aligned range from provider
   -> validate exact byte count and file size
   -> write complete temp file, close, atomically rename into entries/
   -> optional conservative fsync mode
   -> return requested bytes
~~~

Only one fill may exist per block key. The fill owns its context rather than borrowing the first caller's request context. Each waiter can cancel independently; when the last waiter leaves, an unused fill may be cancelled. A successful fill may still finish and publish if another waiter remains. Provider fills are limited globally so a directory of parallel readers cannot exhaust sockets or memory.

The fill result is published only after the exact expected size is received. A short read, extra bytes, mismatched size, or provider cancellation removes the temporary file and returns an error; it never leaves a partial file under a valid cache name.

If the cache cannot write because of permission, disk pressure, or a full budget with every block pinned, a successfully downloaded block is returned to the caller and simply not retained. A cache write failure must not fail an otherwise valid read. The default path does not fsync every block or directory: cache data is disposable, and a torn file left by a crash is detected by its header/payload hash and removed. A conservative fsync mode remains available for deployments that prefer reduced torn-file frequency over write amplification.

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
- Startup removes stale temporary files, validates headers and sizes, and rebuilds budget/LRU in the background. A complete payload is verified against its hash once per process before its first hit.
- On corruption, delete the entry and perform a normal provider fill in the same request. Never return bytes from a corrupt cache file.
- The LRU order can be recovered from block mtime; successful hits touch it in batches rather than updating metadata on every driver read.

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
- Block files are opened relative to a confined root with no-follow semantics and verified with fstat after opening; do not lstat and then reopen by a race-prone path.
- The cache contains plaintext payloads by default. Users who need encrypted-at-rest data should use full-disk encryption; optional per-cache encryption is a later feature.
- cache clear removes references and files, but cannot guarantee physical erasure on SSDs or backed-up filesystems.
- A symlink, hard-link, reparse point, or changed file size in an entry path is treated as corruption; the entry is removed.
- No signed URL, token, password, OAuth code, or HTTP header is stored in a block file.
- Cache-disk exhaustion is a cache miss, not a successful persistence guarantee and not a reason to corrupt an application read.
- Persistent block caching can be disabled per account. The threat model treats the cache as equivalent to an owner-only browser cache or OS page cache; it does not protect from a malicious process running as the same OS user.

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
- ContentVersion change gives a different key and never returns old bytes;
- account ID change or recreated account isolates cache namespaces;
- empty version bypasses persistence;
- reads spanning block boundaries and the final short block;
- EOF boundaries and invalid lengths;
- multiple waiters coalesce into one provider fill;
- one waiter cancelling does not abort a useful shared fill;
- all waiters cancelling cancels unused work;
- fill concurrency limit, provider short reads, oversized responses, and context cancellation;
- every remote request in a multi-request reader remains bound to one revision, including URL renewal, redirects, retries, and conditional range GETs;
- conditional GET returning 200, a bad `Content-Range`, or a mismatched response ETag fails before publishing bytes;
- interrupted temporary writes and startup orphan cleanup;
- crash/torn files survive rename without fsync and are rejected by verification;
- corrupt header, corrupt payload, wrong file length, symlink, and permission errors;
- LRU eviction, pinned readers, Windows deferred deletion, and disk-full behavior;
- FSKit and WebDAV reading the same warm block without a second provider call;
- exact byte comparison against a provider that honors Range but not a whole-file fallback.

Acceptance for enabling it by default:

1. A 10 GiB cache remains under its configured budget during mixed small/large reads.
2. The same unchanged file reopened after daemon restart produces zero provider data calls for cached ranges.
3. A changed provider version produces zero bytes from the previous version.
4. FSKit and WebDAV share one warm fill in the same daemon process.
5. Cache corruption is detected and repaired by a remote refill.
6. Cold and warm path benchmarks show a measurable improvement without unacceptable memory growth or lock contention.
7. Finder/Spotlight/QuickLook do not trigger an unintended full-tree download; disk and in-memory budgets remain inside their configured limits.

## Rollout sequence

1. Correct the ContentVersion scope, define per-request revision binding, and add immediate memory/metadata guardrails.
2. Implement account identity, reconnectable daemon/UDS/XPC control plane, and binary data surface.
3. Complete provider capability conformance and establish latency, relist, QuickLook, and range baselines.
4. Add the cache package with an in-memory index and no integration; exercise it against local fixtures only.
5. Publish and validate block files atomically; add restart, corruption, torn-file, budget, and eviction tests.
6. Wire one cached VFS session into both FSKit and WebDAV in the daemon.
7. Disable duplicate handle-block caches where the shared daemon cache covers the data.
8. Add cache diagnostics, per-account disablement, and an opt-in configuration.
9. Run native PKU read acceptance and only the proven Baidu whole-file digest path.
10. Enable a conservative default after cold/warm, Finder, and soak measurements.

## Explicit first-version limits

- Read-only data only; no write staging or pending-write eviction policy yet.
- No cross-process cache sharing beyond the single-owner daemon.
- No cross-account content deduplication.
- No offline synchronization, conflict merge, or remote invalidation push.
- No per-block encryption or user-supplied cache key in the first release.
- No provider that lacks a trustworthy version should silently use the disk cache.
- The local sample provider should bypass the persistent data cache by default: its bytes already live on local storage and its current open handle is not a remote snapshot.
