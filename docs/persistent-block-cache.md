# Persistent block cache

Status: contract with implementation in `internal/blockcache` and `internal/vfs`, updated 2026-10-30. The daemon owns shared memory and disk caches. The complete delivery and acceptance are tracked in [TODO.md](../TODO.md) and [foundation status](foundation-status.md).

The account, object, path, and inode identities used here are defined by [namespace-contract.md](namespace-contract.md). Cache keys use the implemented account and object identity contract.

## What it is for

The persistent block cache should let a later native-filesystem or CLI operation reuse a range that an earlier operation already downloaded. It is a read cache, not a sync engine, and it should survive daemon restarts without becoming a second source of truth.

Expected wins:

- Reopen a PDF, video, or document without downloading the same 4 MiB ranges again.
- Share warm ranges between native mounts and CLI in the same broker process.
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

The daemon owns one block cache shared across its VFS sessions and native/data endpoints. The FSKit broker client uses that VFS directly without adding a second block cache in the extension. Standalone foreground CLI operations use a process-local memory cache. `daemon ls` and `daemon cat` consume the same target-scoped data endpoint and share the daemon cache with native reads.

The target process shape is:

~~~
                         one broker/daemon process
account store/keychain -> provider.Session -> vfs.Session -> block cache
                                               |
                                  +------------+------------+
                                  |                         |
                            native data endpoint       CLI endpoint
~~~

The complete design has one daemon cache owner shared by the native adapters and CLI. Platform selection and mount mechanics live in their adapters; the cache package depends on the common contracts.

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

A disk hit is allowed only when the caller already has an ObjectRevision with the same ContentVersion used in the key. A changed version chooses a different hash; old blocks become unreachable and are evicted by the 2Q policy. Equal ContentVersion is meaningful only within one account/provider/object ID: two objects may legitimately share a token, but the full key keeps their bytes separate.

This does not make stale metadata disappear. The metadata TTL still controls how long the system may report an older Object. Metadata freshness controls the snapshot selected for a new open; an already opened exact-version handle keeps its own revision and size. Reopening revalidates expired metadata. Native page-cache invalidation must preserve these choices, including concurrent opens of different revisions.

For a range reader, **every remote request** must remain bound to the requested ContentVersion, not only the initial OpenRead. A reader can issue several range GETs, renew signed URLs, follow redirects, and retry; all of them must use the same revision and validate the response. For conditional GET, use `If-Match` with a strong validator, not `If-Range` as the version gate. Validate status 206, `Content-Range`, total size, and response ETag on every response. A 200 full response, ignored condition, mismatched ETag, or disappeared revision becomes `ErrConflict`.

PKU already signs `osdownload` with the requested `rev`, but its renewal and online-edit paths need tests. Baidu's current dlink path does not yet prove that returned bytes are tied to the metadata version, so Baidu range caching must remain disabled. If XPAN MD5 proves reliable, a file no larger than the whole-file threshold may be downloaded completely, hashed, and cached only when that digest matches metadata; this exception must not be generalized to ranges or larger files. MD5 is an accidental-change/compatibility check, not a security guarantee.

The whole-file exception has five rules: the digest must come from provider metadata and be embedded in ContentVersion; it must be compared with the digest saved in the snapshot used by `OpenRead`, not a fresh Stat after download; only a complete-object fill can use it; a mismatch returns `ErrConflict` and publishes nothing; and the provider's digest semantics must first pass modification, duplicate-content, and concurrent-change tests. A locally computed digest is never sufficient by itself. This verifies that the downloaded bytes equal the provider's declaration for the selected content; it does not require discovering a server-side revision identifier and is not a substitute for expected-version writes.

If ContentVersion is empty, persistent caching is disabled for that object. If a provider lacks per-request revision binding, range caching is disabled, but a whole-file entry may still qualify through the five-rule declared-digest path. There is no unauthored metadata-fingerprint fallback in the persistent data cache; a weak fingerprint may only drive short-lived metadata caching.

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

A file name is the hexadecimal key hash plus .blk. The filesystem directory is the initial index; no bbolt database is required. Startup only needs file stat (size and mtime) to establish budget and approximate recency. Header and payload validation can be deferred until first access, so mounting does not wait for a full header scan. Until enough metadata is available, new persistent writes are blocked or treated as an already-full budget. Header and payload hashes are verified on first access in each process and cached in-memory as verified, rather than hashing every small read.

## Read plan

Provisional tuning candidates for measurement (not selected defaults):

| Option | Candidate | Meaning |
| --- | --- | --- |
| block-size | 4 MiB | Alignment for large-file ranges |
| whole-file | 16 MiB | Cache a complete file when its size is at or below this threshold |
| read-ahead | measured, adaptive | Grow within a byte limit during sequential access; shrink after seek |
| fill-concurrency | 4 | Maximum simultaneous provider block fills |
| cache-limit | 10 GiB | Disk budget for cached payloads |
| metadata-ttl | measured per provider | Remote providers should not inherit the current fixed 2 s default; establish the value from Finder relist and provider rate-limit measurements |

The block, whole-file, read-ahead, and TTL values are provisional until baseline measurements cover PKU/Baidu latency, Finder relist calls, QuickLook behavior, and range throughput.

For a file at or below whole-file, the first read can fill the whole file as one cache entry when it has exact revision binding or a declared digest that matches the OpenRead snapshot. A weaker provider without either proof falls back to ordinary uncached reads (or eligible range caching where that is independently proven). For a larger file, reads are aligned to block-size; the final block is shortened to the remaining file length. A read spanning blocks preserves requested byte order; fetching and bounded prefetch follow the measured sequential/seek policy.

On lookup, use an eligible verified whole-file entry or an independently eligible range entry. If whole-file retention exceeds the disk budget, a validated download may still satisfy the read without retention. Range fallback requires its own per-request proof; whole-file eligibility cannot supply it. Whole-file digest verification is an optional object policy, not a mandatory open cost.

For a declared-digest fill, reserve bounded memory/temporary storage, download and hash the entire object against the open snapshot, and only then deliver any bytes through that verified read or publish the entry. A complete entry may subsequently serve arbitrary local slices. The first-byte delay and extra downloaded bytes belong in the whole-file policy measurements.

The per-handle single-block buffer should become unnecessary once the persistent store is present. Use the operating-system page cache for repeated block-file reads. Any application memory L1 participates in the shared byte budget and 2Q policy.

## Hit and fill flow

~~~
driver ReadAt
   -> validate offset/length against Object.Size
   -> compute block key
   -> lookup in-memory index
       hit: open/read local block; verify payload once per process, then return bytes
       miss: join or create one in-flight fill
   -> fetch according to exact-range or whole-object proof
   -> validate exact byte count, snapshot size, and the selected proof
      (whole-object: complete digest comparison before any byte delivery)
   -> write complete temp file, close, atomically rename into entries/
   -> optional conservative fsync mode
   -> return requested bytes
~~~

Only one fill may exist per block key. The fill owns its context rather than borrowing the first caller's request context. Each waiter can cancel independently; when the last waiter leaves, an unused fill may be cancelled. A successful fill may still finish and publish if another waiter remains. Provider fills are limited globally so a directory of parallel readers cannot exhaust sockets or memory.

The fill result is published only after the exact expected size is received. A short read, extra bytes, mismatched size, or provider cancellation removes the temporary file and returns an error; it never leaves a partial file under a valid cache name.

If the cache cannot write because of permission, disk pressure, or a full budget with every block pinned, a successfully downloaded block is returned to the caller and simply not retained. A cache write failure must not fail an otherwise valid read. The default path does not fsync every block or directory: cache data is disposable, and a torn file left by a crash is detected by its header/payload hash and removed. A conservative fsync mode remains available for deployments that prefer reduced torn-file frequency over write amplification.

## Index, 2Q admission, and eviction

The owner process keeps an in-memory entry index:

~~~
keyHash -> path, payload length, file size, lastAccess, refCount, verified, queue
~~~

- lastAccess is updated under a short index lock; it is not a network or disk-I/O lock.
- Access time is flushed to file metadata periodically, not on every 32 KiB driver read.
- The disk byte budget includes headers and payloads; reserve temporary bytes before fills. Separate memory budgets cover retained data, fill buffers, IPC buffers, metadata, and node/reader tables. A concurrency count alone is not a byte budget.
- Byte-budgeted 2Q controls admission and eviction: first-use FIFO entries, reusable entries in a protected LRU, and bounded ghost history for evicted first-use keys. Evict unpinned entries according to this policy until the reservation fits.
- An entry with live readers is pinned. On Unix, unlinking an open file may be safe; on Windows, deletion is deferred until the last handle closes.
- Version changes create new keys. Old-version entries are not eagerly deleted; LRU eventually removes them.
- Startup removes stale temporary files and rebuilds budget/recency from file size and mtime in the background. Header and payload validation are deferred to first hit, where a complete entry is verified once in the process before any cached bytes are returned. A torn or malformed entry is removed and refilled.
- On corruption, delete the entry and perform a normal provider fill in the same request. Never return bytes from a corrupt cache file.
- Recover approximate recency from block mtime with batched updates. Rebuild 2Q reuse and ghost history in memory after restart; persistent policy history is unnecessary for byte correctness.

If a block is larger than the configured budget, bypass persistence. A single account's activity must not be able to evict every block from another active account indefinitely; a later fairness policy can reserve or weight a share per account. Expose per-account usage so fairness can be evaluated with real workloads.

## Access accounting and fetch planning

Correlated small reads during one sequential access count as one reuse event. A thumbnail scan or a stream split into many native read callbacks should not promote every encountered block into the reusable queue. Keep ghost metadata bounded by both entry count and bytes.

Provider fetch size and disk block size are separate controls. Increase bounded read-ahead for sequential access, reduce it after seeks, and cancel unused speculative work. Assemble a persistent entry only after its complete range or whole-object proof is satisfied.

Measure sequential scans mixed with a repeatedly accessed working set, Finder/QuickLook head/tail patterns, first-byte latency, network amplification, queue pressure, and retained/in-flight memory. 2Q adds queue/ghost overhead and must adapt when the hot set changes; these costs are included in the benchmarks.

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
- every remote request in a multi-request range reader remains bound to one revision, including URL renewal, redirects, retries, and conditional range GETs;
- a whole-file fill compares against the digest from the original OpenRead snapshot, not a fresh Stat, and publishes only on an exact match;
- a declared-digest whole-file path does not grant range-cache eligibility;
- conditional GET returning 200, a bad `Content-Range`, or a mismatched response ETag fails before publishing bytes;
- interrupted temporary writes and startup orphan cleanup;
- crash/torn files survive rename without fsync and are rejected by verification;
- corrupt header, corrupt payload, wrong file length, symlink, and permission errors;
- byte-budgeted 2Q admission/eviction, correlated access, scan resistance, pinned readers, Windows deferred deletion, and disk-full behavior;
- native mounts and CLI reading the same warm block without a second provider call;
- exact byte comparison against a provider that honors Range but not a whole-file fallback.

Acceptance for enabling it by default:

1. A 10 GiB cache remains under its configured budget during mixed small/large reads.
2. The same unchanged file reopened after daemon restart produces zero provider data calls for cached ranges.
3. A newly selected revision produces zero bytes from a different revision; an already opened snapshot retains its selected revision.
4. Native mounts and CLI share one warm fill in the same daemon process.
5. Cache corruption is detected and repaired by a remote refill.
6. Cold and warm path benchmarks show a measurable improvement without unacceptable memory growth or lock contention.
7. Finder/Spotlight/QuickLook do not trigger an unintended full-tree download; disk and in-memory budgets remain inside their configured limits.

## Integration and acceptance scope

The cache package, complete daemon, namespace/provider contracts, and native adapters are part of one complete filesystem implementation. Cache unit tests, provider proof tests, go-fuse library tests, and Linux/macOS/Windows native suites have independent targets and report the behavior they verify.

Use controlled immutable fixtures to test storage mechanics and native surfaces to test snapshot/cache coherence. Account identity, the full service lifecycle, global resource accounting, and measured fetch/admission policy are included in that implementation. WebDAV and existing-account migration are deferred to the lowest-priority feature group.

The application-visible snapshot must survive warm hits just as it survives provider reads. The cache cannot substitute a newer version for an existing read handle. Direct I/O is not a baseline or a required implementation step.

## Scope

- Read-only data only; no write staging or pending-write eviction policy yet.
- No cross-process cache sharing beyond the single-owner daemon.
- No cross-account content deduplication.
- No offline synchronization, conflict merge, or remote invalidation push.
- No per-block encryption or user-supplied cache key in the first release.
- No provider that lacks a trustworthy version should silently use the disk cache.
- The local sample provider should bypass the persistent data cache by default: its bytes already live on local storage and its current open handle is not a remote snapshot.
