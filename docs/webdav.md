# Read-only WebDAV

WebDAV is a second access surface over the same provider session used by FSKit. It targets Finder, other desktop file managers, and browser applications. The broker opens the saved account once and serves both surfaces on one loopback listener.

## Start the broker

FSKit and WebDAV are independent and can be enabled together or separately.

```sh
bin/fusedock broker start --account baidu \
  --config "$HOME/.config/fusedock/baidu.broker.credentials" \
  --webdav-auth-file "$HOME/.config/fusedock/baidu.webdav.credentials"
```

For a stable Finder favorite, choose a fixed loopback port:

```sh
bin/fusedock broker start --account baidu \
  --listen 127.0.0.1:17893 \
  --webdav-auth-file "$HOME/.config/fusedock/baidu.webdav.credentials"
```

The default is an ephemeral port. A fixed port survives broker restarts, while the read-only token is still regenerated and written to the private auth file. The listener cannot be changed to a wildcard or LAN address.

For WebDAV only, omit `--config`. For FSKit only, omit `--webdav-auth-file`.

The broker prints the WebDAV endpoint. The auth file contains the exact values needed by a WebDAV client:

```text
endpoint=http://127.0.0.1:PORT/webdav/
username=fusedock
password=RANDOM-READ-ONLY-TOKEN
scope=/
```

The file is mode `0600` and is removed when the broker exits. The token is generated for this broker process and grants read-only access only to the selected account's configured scope.

## Finder

1. Start the broker and leave it running.
2. In Finder choose **Go → Connect to Server**.
3. Enter the `endpoint` value from the auth file, for example `http://127.0.0.1:54321/webdav/`.
4. Choose registered user, enter the `username` and `password` values from the auth file.
5. The volume is read-only.

The server advertises DAV class 1. It deliberately does not advertise write locks: writes, creation, deletion, rename, and property updates return `405 Method Not Allowed`. This lets Finder present a read-only volume instead of pretending write support exists.

## Scope a token to a subtree

```sh
bin/fusedock broker start --account baidu \
  --webdav-auth-file "$HOME/.config/fusedock/baidu-docs.webdav.credentials" \
  --webdav-scope /Documents
```

The WebDAV root then maps to `/Documents`. Paths are resolved through provider `List` calls beneath that object; `..`, duplicate names, and names that leave the selected provider root are rejected. The generated token cannot access other accounts or the rest of the provider namespace.

## Browser access

Browser code cannot use Basic auth for arbitrary `fetch` requests without prompting and may be blocked by CORS. Allow an exact trusted origin and use the token as a bearer capability:

```sh
bin/fusedock broker start --account baidu \
  --webdav-auth-file "$HOME/.config/fusedock/baidu.webdav.credentials" \
  --webdav-allow-origin http://localhost:3000
```

```js
const authorization = "Bearer " + token;
await fetch(endpoint + "path/to/file", {
  headers: { Authorization: authorization, Range: "bytes=0-65535" },
});
```

The server allows `GET`, `HEAD`, `PROPFIND`, and `OPTIONS` from the configured origin. It never enables wildcard CORS and rejects any request whose `Origin` is not in the allowlist. Native clients without an `Origin` header use Basic auth or `Authorization: Bearer`.

## Finder reports “additional users”

macOS sometimes shows:

```text
This file server will not allow any additional users to log in. Please try again later.
```

This is a misleading NetAuthAgent message for error number 61, `ECONNREFUSED`. It normally means the broker is no longer running or Finder is using an endpoint from a previous broker process. Ephemeral endpoints change on every broker start.

Check that the broker terminal is still running, then reconnect using the current `endpoint` from the auth file. For a persistent Finder connection, restart the broker with a fixed `--listen 127.0.0.1:PORT` address and remove the old Finder favorite. A stale favorite for a stopped broker will keep producing this message even though neither authentication nor the WebDAV server is reachable.

## Semantics

- `GET`/`HEAD` support byte ranges through Go's HTTP content server and the provider's `ReadAt` implementation.
- `PROPFIND` reports files and collections with size, modification time, resource type, and a version-based ETag.
- The current `ETag` is derived from provider object ID and the weak `Version` hint. It must not be treated as proof of byte-identical content. The target contract emits a strong ETag only for a strong ContentVersion; otherwise it emits a weak ETag or omits it.
- `PROPFIND` with `Depth: infinity` is rejected so Finder and third-party clients cannot trigger an unbounded tree walk.
- WebDAV paths are presentation paths. Object identity remains the provider's stable object ID.
- The current surface is read-only. `PUT`, `POST`, `DELETE`, `MKCOL`, `COPY`, `MOVE`, `PROPPATCH`, `LOCK`, and `UNLOCK` are rejected before reaching the file system.
- The token is process-scoped and read-only. Restarting the broker generates a new token; clients must reconnect.
- The listener remains loopback-only. Proxy environment variables are not involved because this is an inbound server.

## Tests

```sh
go test -race ./internal/webdavservice ./internal/cli
```

The tests exercise Basic and Bearer auth, unauthorized requests, DAV class 1, PROPFIND, subtree scoping, exact byte ranges, read-only method rejection, CORS preflight, allowed origin enforcement, and cleanup of the temporary auth file.

Native acceptance was also run with macOS `mount_webdav -i`: the system client mounted the scoped Baidu subtree, listed its files, read a file successfully, and unmounted without write access.
