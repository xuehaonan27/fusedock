# Roadmap

Updated 2026-10-28. [TODO.md](../TODO.md) owns the engineering plan. Work is organized by responsibility and accepted as complete functionality, without reduced daemon stages.

## Filesystem product

FuseDock exposes cloud storage as a filesystem through Linux FUSE, macOS FSKit, and Windows WinFsp. Provider, namespace, VFS, cache, and service contracts are platform-independent; build constraints select each native adapter. CLI and future application surfaces consume the same contracts.

A version-bound read snapshot is a valid filesystem abstraction and remains part of the design. Its bytes, size, open/close lifetime, native data caching, and concurrent-open behavior must agree across the complete path. File paths are mutable directory entries; object identity survives rename. A configured scope is resolved at each mount and held as an object for that mount.

## Complete foundation delivery

- Immutable AccountID and explicit object/version identity for new bindings; stable CredentialRef independent of aliases.
- Full daemon control/data APIs, authenticated reconnect, service and mount lifecycle, reauthentication, diagnostics, and resource management.
- Provider-specific exact range or declared-digest whole-file proof; complete-file hashing is optional rather than a universal open cost.
- Global byte/concurrency budgets, byte-budgeted 2Q, correlated-access accounting, adaptive bounded read-ahead, measured metadata policy, and persistent cache storage.
- Native snapshot and cache coherence using normal platform caching; no direct-I/O prerequisite.
- Separate core, go-fuse library, Linux, macOS, and Windows test suites with compile-time platform routing.

Implementation dependencies are tracked within that complete scope. Test separation does not divide the daemon into reduced releases. See [the experiment record](prerequisite-test-results.md) for evidence already collected.

## Product coverage

Multi-account views, Google Drive, OneDrive, S3, iCloud access investigation, safe writes, native application hosts, packaging, and the external provider SDK remain explicit product work. Writes require their own expected-version and recovery semantics.

## Lowest-priority features

The existing WebDAV code has been hard-deleted. A future WebDAV server is a fresh implementation against the shared contracts; a WebDAV client provider is also deferred here.

Existing-account migration is deferred with WebDAV. Its intended scope is preserving saved login references while adding AccountID to configuration, without moving system credential items. Neither WebDAV nor migration blocks the filesystem foundation.
