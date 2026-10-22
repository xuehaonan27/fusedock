# Roadmap

The ordering is based on validating core contracts before multiplying surfaces and provider-specific behavior.

## 0.1 Read-only vertical slice

- Provider registry with descriptor-driven CLI discovery.
- Local reference provider using `os.Root` confinement.
- PKU IAAA and AnyShare read path.
- Metadata request coalescing and per-handle block reads.
- Linux/macOS read-only FUSE driver.
- Cross-platform builds for Linux, macOS, and Windows.

## Next: productionize the read path

- Run native FUSE acceptance on Linux and FSKit mount acceptance on macOS.
- Add bounded disk cache keyed by account, object ID, version, and range.
- Add session persistence in an owner-only store with atomic refresh-token rotation.
- Add account selection and a multi-account virtual root.
- Add mount supervision and clean diagnostics for stale sessions.
- Add opt-in PKU live acceptance with a dedicated test account.

## Then: safe writes

- Define mutation capabilities and provider revision semantics.
- Add private write staging, `fsync`/close publication, and recovery journals.
- Support create, overwrite, truncate, append, mkdir, remove, and same-provider rename.
- Return `EBUSY` on a revision conflict and `EXDEV` across account or provider boundaries.
- Add provider fixtures for interrupted uploads and concurrent remote edits.

## Platform coverage

- Add an NFS bridge as a macFUSE-free fallback.
- Add a WinFsp adapter through cgofuse with native Windows mount tests.
- Add a background service or tray supervisor for each desktop platform.
- Add authenticated local GUI and native host adapters.
- Reuse provider/VFS services on mobile; do not expose a FUSE mount where the OS does not support it.

## Provider coverage

- Google Drive: OAuth with PKCE, shared-drive and shortcut semantics, Docs export decisions.
- Baidu NetDisk: OAuth, pagination, rate limits, multipart ranges, and cloud-specific file types.
- Additional S3-compatible and WebDAV providers as lower-risk protocol implementations.
- Provider conformance suite shared by built-in and external adapters.

Each provider must state its exact read/write behavior. A common API does not make Google Docs, Baidu archives, and ordinary files semantically interchangeable.

## External provider SDK

- Specify a versioned sidecar protocol with capability negotiation.
- Preserve context cancellation, random-access reads, error classes, and secret isolation.
- Support Go in-process providers first; add non-Go sidecars only after conformance tests exist.
- Require explicit local installation and authentication; do not auto-download executable providers.
