# Namespace contract

Status: design proposal. The current release has one provider session per mount and uses the saved account name as its only account identifier. This contract defines the identity model required for shared VFS state, multiple accounts, stable inode numbers, and the persistent block cache.

## Purpose

FuseDock has three namespaces that must not be conflated:

| Namespace | Answers | Stable identity |
| --- | --- | --- |
| Account | Which saved provider binding is this? | `AccountID` |
| Remote object | Which provider object and revision? | `NodeKey` plus `ObjectRevision` |
| Surface path | How does this mount, WebDAV root, or CLI display it? | None; paths are presentation |

The contract has four rules:

1. A mutable display name or path never becomes a durable identity.
2. An account ID never survives a change to its remote principal or provider binding.
3. An object ID is scoped to an account binding; an identical remote ID under two accounts is two different objects.
4. A cached byte range is valid only for the exact object revision requested by the caller.

These rules apply equally to CLI access, Linux FUSE, macOS FSKit, WebDAV, the future Windows driver, and any mobile UI. A surface may choose which part of the namespace it exposes; it may not invent a different identity for the same object.

## Core types

The following Go shapes express the contract. They are a design target, not APIs already present in this release.

```go
type AccountID string
type ProviderID string

type NamespaceKey struct {
    AccountID  AccountID
    ProviderID ProviderID
}

type NodeKind uint8

const (
    NodeRoot NodeKind = iota + 1
    NodeAccount
    NodeRemote
)

type NodeKey struct {
    Kind       NodeKind
    Namespace  NamespaceKey
    ObjectID   string // set only for NodeRemote
}

type ObjectRevision struct {
    Node           NodeKey
    ContentVersion string
    Size           int64
}

type ResolvedObject struct {
    Node   NodeKey
    Object provider.Object
}
```

`NodeRoot` represents the synthetic root of a surface that combines several accounts. `NodeAccount` represents a saved account beneath that root. `NodeRemote` identifies an object returned by a provider, including the provider's root object. Mapping a provider root to a single-account mount's `/` changes the surface path, not its node key.

`ObjectRevision` is an internal snapshot. It is not accepted as an untrusted identity from a remote service. The namespace layer constructs it from a resolved provider session and object.

## Account identity

Every saved account has an immutable `AccountID` in addition to its human-facing alias.

Format and creation rules:

- The value is `acct_` followed by 32 lowercase hexadecimal characters, representing 128 random bits.
- It is generated with a cryptographic random source and is never derived from the alias, provider, username, endpoint, timestamp, or credential hash.
- It must be unique among saved accounts and must not be recycled from any application-managed pool. Random 128-bit generation makes accidental reuse after removal negligible.
- The account store is the only component allowed to allocate it. A caller cannot choose an ID.
- It is an opaque identifier, not an authentication capability. Knowing an account ID grants no access.

`AccountID` identifies a saved provider binding, not merely a row in a UI list. Each provider declares the fields that affect remote identity. The initial binding is:

```text
provider ID + normalized endpoint + remote principal + identity-affecting options
```

The remote principal is preferably an OAuth issuer + stable subject/openid. Providers without one may fall back to username, with a weaker documented guarantee. A versioned, non-secret binding fingerprint is stored beside `AccountID`; it is checked at load time but does not participate in random ID generation.

The following changes preserve the ID:

- changing the alias;
- changing whether the account is the default;
- rotating a password, OAuth token, or saved provider session for the same binding;
- replacing a credential with an equivalent one for the same binding;
- restarting the daemon or application.

The following changes require a new `AccountID`:

- changing provider;
- changing a declared binding field, including endpoint, remote principal, or identity-affecting option;
- deleting an account and adding it again, even under the same alias;
- changing credentials in a way that binds the alias to a different remote principal.

The provider enumerates the fingerprint fields explicitly. Adding an unrelated presentation or tuning option must not rotate the account automatically. A fingerprint mismatch at load time is a fail-closed state: the account needs explicit reauthentication rather than silently reusing old node and cache identity.

If an add-with-replace operation changes the binding, the store must allocate a new ID. It must not keep the old ID merely because the alias matches. Data, inode caches, and capabilities associated with the old ID become unreachable; cache reclamation is best effort.

### Alias rules

The alias is the user-facing path component under an aggregate root. It is mutable in principle but is not an identity.

- Aliases remain unique. Uniqueness is case-insensitive on every platform so `Work` and `work` cannot resolve differently on macOS and Linux.
- An alias is a portable single path component: it contains no `/`, `\`, NUL or control characters, is not `.` or `..`, does not end in a dot or space, and is not a reserved Windows device name.
- `.fusedock` and a `.fusedock-` prefix are reserved for FuseDock's virtual namespace.
- An alias rename preserves `AccountID`. Credential-store items are keyed by `AccountID`, so rename does not move secrets.
- Account removal and re-creation under the same alias produce different IDs.

The CLI may continue to accept aliases for convenience. Its JSON output and diagnostics should also expose the immutable ID. Automation that must survive a rename should use the ID.

## Account-store migration

Adding identity requires a versioned migration from the current config format.

1. Acquire the exclusive account-store lock.
2. Read and validate the old config without assigning temporary IDs.
3. Validate aliases under the new portable and case-insensitive rules. Fail with an actionable conflict if existing aliases cannot be represented unambiguously.
4. Generate one random ID per account with collision checks and compute the versioned binding fingerprint.
5. Copy or rekey credential-store items from alias keys to `AccountID` keys idempotently; retain the old item until the move succeeds.
6. Write a new config version using an owner-only temporary file, `fsync`, atomic rename, and directory sync.
7. Release the lock only after the migrated record and credential lookup are durable.

Migration must fail closed. A process must not proceed with ephemeral IDs that would change on the next start. The old config and credential-store entries remain readable if migration fails.

The account listing may expose the ID, alias, provider, and username. It must not expose secrets, provider state, or credential-store locators.

## Remote object identity

A namespace binds exactly one authenticated provider session. The namespace layer validates every object returned by that session before exposing it to a driver.

For `NodeRemote`, the provider must satisfy these rules:

- `Object.ID` is non-empty and stable across provider session restarts while the account binding is unchanged.
- The same provider object keeps its ID across display-name changes when the provider has a native stable ID.
- An ID is never reused for different content with an unchanged `ContentVersion`.
- An ID is unique within its account and provider namespace. It need not be globally unique.
- `Object.ID` is opaque. The namespace layer does not parse a path, account alias, URI, or provider-specific structure out of it.
- `Object.ParentID` names the parent object in the same provider session. It is not a local inode and may be empty only for the provider root.
- `Object.Name` is a validated presentation component. It is not used for cache identity or persisted in cache paths.

The current provider comment guarantees an ID only for the lifetime of a session. Durable inode identity and a restart-safe cache require the stronger guarantee above. Providers that cannot meet it remain usable with an in-memory-only policy, but they must not advertise durable object identity or persistent caching.

### Revision rules

`Object.ContentVersion` identifies the readable revision. For a file:

- Within the same `AccountID + ProviderID + ObjectID`, equal non-empty `ContentVersion` and equal size imply byte-identical content. Different objects may share a ContentVersion.
- `ContentVersion` changes whenever the bytes available from any read can change.
- `OpenRead(ctx, revision)` either serves that version or returns `provider.ErrConflict`.
- It must never use the object ID to open the current remote file and attach an older version string to those bytes.
- The returned range and total size must agree with `object.Size`.
- A missing ContentVersion is allowed for ordinary current-revision reads, but disables persistent caching for that object.

Revision binding applies to **every remote request**, not only the initial open. Range GETs, redirects, signed-URL renewals, retries, and conditional metadata requests made by the Reader must use the same ContentVersion. Conditional range reads use `If-Match` with a strong validator; they do not use `If-Range` as the exact-version gate. Every response must validate status 206, `Content-Range`, full size, and response ETag. A server that ignores the condition, returns 200 full content, or returns a different version produces `ErrConflict`.

If a caller has a stale object and the provider cannot retrieve that revision, it returns `ErrConflict`; it does not silently refresh and return different bytes. A higher layer may re-stat and retry before returning any bytes, but the retry must create a new `ObjectRevision`.

Persistent caching is enabled only when the provider declares durable object identity, a strong ContentVersion, per-request revision-bound reads, and reliable ranges. Factory declares an upper bound, session probing narrows it, and an object can opt out. Existing providers use optional capability interfaces; absence of a revision interface means no persistent cache. The contract needs conformance tests before the cache is enabled.

PKU already binds `osdownload` to the requested `rev`, but signed-URL renewal and online-edit paths still need explicit tests. Baidu currently obtains a `dlink` without proving that it is tied to the metadata revision, so its range cache remains disabled even though its generated version contains timestamp, size, and MD5. If that MD5 proves reliable in live tests, a whole-file entry below a small threshold may be downloaded fully, hashed, and cached only when the digest matches; this exception does not extend to ranges or larger files.

## Surface namespaces

A surface binds to a target, not merely to a string path. The target selects either one account or an aggregate view:

```go
type ViewMode uint8

const (
    SingleAccount ViewMode = iota + 1
    AggregateAccounts
)

type MountTarget struct {
    View      ViewMode
    AccountID AccountID // required for SingleAccount
    Scope     []string  // provider-relative display path
}
```

- A single-account mount exposes the selected provider root or scope at `/`.
- An aggregate root exposes each saved account as `/<alias>`; the account directory is a synthetic `NodeAccount`, and descendants are remote nodes from that account's session.
- Account aliases are the only first-level entries owned by the aggregate root. Provider IDs are not separate top-level directories, so two accounts using the same provider remain independent.
- A scoped WebDAV or FSKit view uses the same rules below its selected root. An FSKit volume UUID is derived with UUIDv5 from a versioned encoding of AccountID and normalized scope; duplicate mounts of the same account + scope are rejected.
- A mount configuration may have its own local mount identifier, but that identifier never becomes part of remote object identity or the data-cache key.

Path resolution has two stages. First the surface selects the account binding; then the provider resolves the remaining components by listing and comparing validated names. A path is never concatenated into a provider API path and never substituted for an object ID.

The result is a `ResolvedObject`. The local driver keeps its own node table and presentation attributes, but all provider reads use the resolved `NodeKey` and exact `ObjectRevision`.

Paths are allowed to change. Renaming an account alias, reordering a mount list, or exposing the same account under a different scope does not change remote identity or cached bytes.

### Collisions and stale names

- An alias collision is an account-store error.
- A duplicate provider name in one directory remains a provider error.
- If two names collide under the target filesystem's case or Unicode normalization rules, the surface must not silently merge them or choose one. It fails the affected lookup with an actionable namespace error. A future escaped-name layer may make both visible, but it must map display names back to the original objects explicitly.
- A stale path may resolve to `ErrNotFound`; a previously resolved node whose account has been removed is stale and must not fall back to another account with the same alias.
- If `OpenRead` reports `ErrConflict`, the old revision is not readable. The path may be re-resolved and retried once before any bytes are returned, but a caller that already received old bytes is never silently switched to a different revision.

## Node and inode identity

Inode or FSItem identity is derived from `NodeKey`, never from display name or bare provider object ID.

```text
nodeEncoding =
  "fusedock-node-v1" || NUL ||
  uint8(kind) ||
  lengthPrefixed(namespace.accountID) ||
  lengthPrefixed(namespace.providerID) ||
  lengthPrefixed(objectID)
```

The encoding is a stable key, not a mandate to hash it into 64 bits. FUSE maintains a NodeKey-to-node map whose IDs are stable within a mount; protocol nodeid and user-visible `st_ino` are modeled separately until their relationship is verified. FSKit may use persistent identifiers only if the daemon maintains a durable NodeKey-to-identifier table and can resolve any returned persistent identifier. A hash plus an in-memory collision check is not a persistent lookup table.

The current FUSE and FSKit code hashes only `Object.ID`. That is insufficient once two accounts can expose the same ID. Before aggregate mounts, FUSE must use a namespaced node map and FSKit must either set `supportsPersistentObjectIDs = false` or implement the durable mapping described above.

Version is deliberately absent from `NodeKey`: a content update changes the revision, not the object's identity. An account removal followed by a new account with the same alias produces a different node key.

## Cache identity

The persistent cache consumes the namespace contract rather than reconstructing identity from mount paths.

```text
namespaceTag = SHA-256(
  "fusedock-namespace-v1" || NUL ||
  lengthPrefixed(accountID) ||
  lengthPrefixed(providerID)
)

blockKey = SHA-256(
  "fusedock-block-v2" || NUL ||
  lengthPrefixed(accountID) ||
  lengthPrefixed(providerID) ||
  lengthPrefixed(objectID) ||
  lengthPrefixed(contentVersion) ||
  uint64(fileSize) || uint64(offset) || uint64(length)
)
```

All integers are fixed-width big-endian values to avoid delimiter ambiguity. `namespaceTag` contains no readable alias, username, endpoint, or remote name. It lets the cache account for and clear one account without scanning every payload.

The cache stores entries below a namespace-tagged directory and validates the full key hash in each block header. A cache hit requires an already resolved `ObjectRevision`; no lookup treats a path name as sufficient identity.

The endpoint is not a separate key component because it is part of the account binding, and changing it rotates `AccountID`. A cache layer that ever preserves an account ID across a binding change violates this contract.

## Process and surface boundaries

- Each account has one provider session, refresh lock, metadata cache, node table, and cache namespace. Sessions are not shared merely because credentials or endpoints look equivalent.
- A broker data capability is bound to one `MountTarget`. The server attaches or verifies the account namespace itself; a client cannot select a different account by changing an object ID in a request.
- FSKit and WebDAV may share a provider and VFS session when they expose the same target. They must not each construct a separate cache namespace for that target.
- The CLI may open a saved account temporarily, but it uses the same account ID and namespace rules. A temporary process must not create a second writer for a daemon-owned cache.
- Aggregate and single-account views may coexist, but an object's remote identity is the same in both. Only its surface path and inode mapping differ.
- Removing or rebinding an account invalidates open capabilities for that target. Existing handles fail with a stale/not-found error; they never continue against a replacement alias.

The current broker protocol contains a full `provider.Object` and is bound to one process session. Multi-account support needs an explicit protocol revision so the server can validate the target rather than trusting client-supplied account fields.

## Required acceptance cases

The namespace implementation is not complete until these cases pass:

- A version-1 account store migrates to persistent random IDs; reload returns the same IDs.
- Migration failure leaves no partially published config and never exposes ephemeral IDs.
- Two accounts may expose the same provider object ID without sharing inode numbers or cache entries.
- Removing and recreating an alias produces a new account ID and cannot read either old cache entries or old capabilities.
- Reauthentication with an unchanged binding preserves the ID; changing username, endpoint, provider, or options rotates it.
- Alias uniqueness is case-insensitive and portable across Linux, macOS, and Windows.
- Single-account and aggregate views resolve the same remote object to the same `NodeKey`.
- A path rename changes presentation only; it never changes the cache key.
- A changed ContentVersion cannot return old cached bytes, and an exact-version read failure returns `ErrConflict`.
- An empty version disables persistent caching for that object.
- Provider short reads, wrong sizes, and revision mismatches never publish a cache entry.
- FSKit and WebDAV use the same account namespace when exposing the same target.
- A broker capability for one account cannot read an object from another account.
- A node-map collision is detected rather than silently assigning one identifier to two nodes.

## Implementation order

1. Immediately disable FSKit persistent object IDs and bound per-handle memory.
2. Add `AccountID`, binding fingerprint, `AccountID`-keyed credentials, versioned account-store migration, alias validation, and rotation rules.
3. Introduce a reconnectable daemon/UDS/XPC control plane before claiming persistent platform identity.
4. Revise and conformance-test provider stable-ID and per-request revision-bound reads.
5. Add a namespace resolver that scopes a provider session to an `AccountID` and returns `NodeKey` plus `ObjectRevision`.
6. Replace bare-object identity in FUSE and FSKit with explicit NodeKey mapping; derive deterministic volume identity and test extension restart.
7. Bind broker and WebDAV capabilities to a single account target; add the aggregate root after the scoped path works.
8. Add namespace-tagged cache directories, then implement the persistent block cache after measured metadata/memory baselines.
9. Add writes only after object identity, revision conflicts, and recovery journals are tested.

The first implementation can keep the current one-account CLI and mount behavior. It should still introduce identity below the surfaces so that adding a second account does not require changing cache or inode semantics later.
