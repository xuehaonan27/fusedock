# Account broker security review

The broker gives the sandboxed FSKit extension access to an authenticated provider session without copying the account password into the extension. It adds a local read-only service, so its trust boundaries and residual risks are explicit.

## Assets and trust boundaries

- The current account metadata file contains provider IDs, usernames, endpoints, and the default account. New saved bindings also separate AccountID from CredentialRef; existing-account conversion is deferred and does not move credential items. It contains no secrets.
- The password, OAuth credentials, and renewable provider session remain in Keychain, Secret Service, or Credential Manager. Only the CLI/broker process reads them.
- The broker holds the provider session in memory and exposes metadata plus byte-range reads.
- The FSKit extension holds only a temporary in-memory capability. It never receives a password, refresh token, account list, or credential-store API.
- Untrusted remote object names and IDs still cross into the extension, so the provider contract's name/ID validation remains mandatory.

```text
credential store
      | (CLI process only)
  account broker
      | authenticated loopback, read-only objects
  FSKit extension
      |
  kernel / Finder
```

## Controls

- The listener binds specifically to `127.0.0.1` with an ephemeral port. It does not bind a wildcard or LAN interface.
- The capability file is created atomically with mode `0600` inside a directory requiring owner-only permissions.
- Windows private directories receive a protected current-user DACL. Daemon control uses a local named pipe with the current-user SID; the client also verifies the server process SID. The standalone credential-file reader on Windows remains unsupported.
- The disk capability is bootstrap-only and can be claimed exactly once. A successful claim issues a new random 256-bit data capability that exists only in broker and extension memory.
- Data endpoints accept only the claimed session capability. Reusing the bootstrap capability for root, list, stat, or read returns `401`.
- `close` revokes the in-memory capability; the CLI removes the bootstrap file immediately after a successful claim and again on broker exit.
- Authorization comparison uses constant-time comparison. Tokens are never printed, put in URLs, or logged.
- Protocol v2 operations are POST requests with JSON input. Reads return binary payloads. Objects are resolved server-side; reads use opaque handles and explicit release. There is no mutation route; unknown routes, including write-like paths, return `404`.
- The server rejects any non-empty `Origin` or `Referer`, enforces the exact loopback Host value, caps request headers/body, and caps a single read at 16 MiB.
- The client requires an HTTP URL with a literal loopback IP and port. It rejects DNS names, HTTPS downgrade ambiguity, paths, queries, user info, and redirects.
- The client transport ignores proxy environment variables, so HTTP proxy settings cannot route object requests away from loopback.
- Provider errors are translated to fixed public codes and messages. Remote error text, signed URLs, credentials, and object contents are not returned in error bodies.
- Broker metadata/file access is read-only. It cannot manage accounts or mount a different namespace than the account selected at startup.

## Threat review

| Threat | Result |
| --- | --- |
| Other OS users read the capability file | Blocked by owner-only directory/file permissions. |
| Remote hosts connect to the broker | Blocked by binding to `127.0.0.1` only. |
| Browser page sends a cross-origin request | Blocked by non-empty Origin/Referer rejection and bearer capability. |
| Extension reads account passwords or refresh tokens | No credential route exists; the broker exposes object operations only. |
| Bootstrap token replay after mount | Claim succeeds once; later bootstrap claims fail. |
| Stale capability after broker exit | Data capability is process-memory-only; the config file is removed on clean exit. |
| Proxy or redirect exfiltrates request headers | Proxy disabled; HTTPS/redirects/non-literal endpoints rejected. |
| Caller invokes a write operation | No mutation protocol exists. |
| Oversized read or malformed request consumes memory | JSON/read/header limits and range validation. |
| Token appears in process arguments or logs | Token is generated in memory and written only to the private capability file; CLI output omits it. |

## Residual risk

The operating system does not provide a portable way to prove over loopback TCP that the caller is a specific sandboxed extension. A same-user process can read the private capability file before the extension claims it or race the one-use claim. If it wins, it can obtain read-only access to the selected account until the broker stops or the capability is revoked. It cannot obtain the password, refresh token, write access, account-management access, or access to a different account.

This is a same-user capability, not an elevation to another user. It still increases exposure compared with no listener, because a process that can read the file needs no additional authentication prompt. The one-use claim, in-memory session token, read-only protocol, broker lifetime, and file deletion limit the window.

The one-use file also has a lifecycle gap: the system can terminate and relaunch the FSKit extension. A claimed token then disappears with the old extension process, while the mount resource still needs data. The planned fix is not another longer-lived file token. The daemon and extension must use a UDS/XPC control channel with peer identity and code-signing checks, allowing the extension to reconnect after relaunch. Until that exists, restart behavior remains a known availability limitation rather than a solved property of the broker.

For release hardening, the next broker transport should be a Unix domain socket or XPC service with peer code-signing checks, plus a launchd-managed broker lifecycle. The current loopback protocol remains useful because FSKit's extension sandbox and Go bridge can use it without weakening the Keychain ACL or embedding credentials.

The persistent cache implemented in `internal/blockcache` stores plaintext file payloads under an owner-only directory. Its threat model is comparable to an owner-only browser cache or the OS page cache: it does not protect data from a malicious process running as the same operating-system user. Each daemon mount can disable persistent retention with `--persistent-cache=false`; encryption whose key is also available to the same user would not change that threat boundary.

This review does not claim zero additional attack surface. It establishes that the broker does not cross the account or OS-user boundary and that its new capability is read-only and short-lived.

## Daemon and persistent storage implementation

- Control uses a private Unix socket with peer UID checks on Linux/macOS, or a current-user named pipe on Windows. The control endpoint is not a network listener. These checks authenticate the OS user; macOS code-signing identity checks remain unfinished.
- Each mounted scope has a resolved target. Data requests accept server-observed object IDs and revisions. Removing or rebinding an account revokes its target and data endpoints when the account watcher observes the change (currently every two seconds).
- New account bindings receive a random AccountID and separate CredentialRef. Refresh persistence matches immutable binding identity, including after alias rename. Old sessions cannot update a replacement account.
- One daemon holds the disk-cache ownership lock. Cache entries use namespace-separated hashed paths, complete temporary writes, root-relative atomic publication, and header/key/length/payload validation before a cold hit is delivered.
- Unix entries must be owned by the user, private, regular, and single-linked. Windows entries reject non-regular, multiply-linked, and reparse files; directory ACLs restrict the current user.
- Cache data is plaintext and readable by the same OS user. The shared memory budget includes retained and in-flight block payloads. Native kernel caches, object tables, metadata in-flight allocations, and all process overhead are not yet covered by a complete global byte ledger.

The new unit and integration tests use local fixture providers and fake credential stores. They do not perform a new real-account security audit or qualify Baidu digests for persistent reuse.
