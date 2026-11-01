# FuseDock

FuseDock turns cloud storage into local file systems through a provider-neutral core. A provider implements a small read contract once; the same provider can then serve CLI inspection, FUSE mounts, and WinFsp, and future GUI and mobile surfaces.

The repository contains a shared namespace, read VFS, persistent block cache, account daemon, and native adapters for Linux FUSE, macOS FSKit, and Windows WinFsp. Built-in providers cover local directories, PKU NetDisk, and Baidu NetDisk. Saved accounts keep credentials in the host credential store. The sandboxed FSKit extension reads through the account broker. Implementation and acceptance results are tracked in [foundation status](docs/foundation-status.md).

## Current status

| Area | Status |
| --- | --- |
| Provider contract | Versioned read-only contract in `pkg/provider` |
| Local provider | Implemented and tested |
| PKU NetDisk | Implemented for read-only IAAA login, namespace traversal, range reads, and renewable sessions |
| Baidu NetDisk | Implemented for read-only OAuth device login, namespace traversal, range reads, and token refresh |
| Account store | Random AccountID and stable credential reference for new bindings; host credential storage |
| Daemon/cache | Shared cache owner, local control API, mount restoration, memory/disk 2Q, cache administration |
| Account broker | One-use loopback capability; secrets stay in the CLI/broker process |
| Linux FUSE | Native snapshot/read/rename/unmount contract passed on the Actions FUSE 3 runner |
| macOS FSKit | macOS 26 compatibility lifecycle; macOS 27 cache lifecycle; see [platform acceptance](docs/foundation-status.md) |
| Windows | Native WinFsp snapshot/read/rename/unmount contract passed on the Actions runner |
| Google Drive | Not implemented yet; fits the same provider contract |
| Writable mounts | Not implemented; the capability and conflict model is defined in the architecture |
| Multi-account namespace | Not implemented; a session currently represents one account |

## Quick start

Go 1.25 or newer is required. The cache uses root-relative rename and timestamp APIs added in Go 1.25. Building the FSKit application requires Xcode 26 or newer; the application runs on macOS 26 or newer. Xcode 27 builds include both OS implementations.

```sh
go build -o bin/fusedock ./cmd/fusedock
bin/fusedock providers
```

On Linux, expose a local directory through the same provider and FUSE path used by remote providers:

```sh
mkdir -p /tmp/fusedock-local
bin/fusedock mount local /tmp/fusedock-local --option root="$HOME/Documents"
```

In another terminal:

```sh
ls /tmp/fusedock-local
```

On Windows, WinFsp creates a directory mountpoint itself: choose an unused drive letter or a path whose parent exists and whose final directory does not yet exist.

Press Ctrl-C in the mount terminal to unmount. The Linux FUSE 3 package must provide `/dev/fuse` and `fusermount3`.

Inspect provider contents without mounting:

```sh
bin/fusedock ls local --option root="$HOME/Documents"
bin/fusedock ls local --option root="$HOME/Documents" --json
```

## Mount PKU NetDisk

The PKU provider signs in through IAAA and exposes the account's accessible AnyShare document libraries at the mount root. Saved accounts keep the password and renewable session in the system credential store, never in the account metadata file.

Interactive password entry:

```sh
mkdir -p /tmp/fusedock-pku
bin/fusedock mount pku /tmp/fusedock-pku --username YOUR-CAMPUS-ID
```

For repeatable local use, add a saved account. The password prompt stays out of shell history and command arguments:

```sh
bin/fusedock account add \
  --provider pku \
  --username YOUR-CAMPUS-ID \
  --name pku \
  --default
bin/fusedock account ls
bin/fusedock ls --account pku
```

The existing owner-only bootstrap-file flow remains available for one-time migration:

```sh
./scripts/setup-pku-credentials.sh
bin/fusedock ls pku --credential-file "$HOME/.config/fusedock/pku.credentials"
```

Import it into the system credential store, verify the saved account, then remove the bootstrap file:

```sh
bin/fusedock account add \
  --credential-file "$HOME/.config/fusedock/pku.credentials" \
  --name pku --default
bin/fusedock ls --account pku
```

After migration, the CLI and FSKit broker use the system credential store. See [accounts](docs/accounts.md). Alternatively, pass a credential from an environment variable:

```sh
export FUSEDOCK_PKU_PASSWORD='your-password'
bin/fusedock mount pku /tmp/fusedock-pku \
  --username YOUR-CAMPUS-ID \
  --credential-env password=FUSEDOCK_PKU_PASSWORD
```

CAPTCHA and OTP/SMS challenges are prompted in the terminal. A CAPTCHA image is written to a mode-0600 temporary file and removed after the answer is entered. The provider can be pointed at a compatible AnyShare service for development:

```sh
bin/fusedock providers
bin/fusedock mount pku /tmp/fusedock-pku \
  --option endpoint=https://disk.pku.edu.cn \
  --option iaaa_endpoint=https://iaaa.pku.edu.cn \
  --username YOUR-CAMPUS-ID
```

The public login page and its challenge parsing can be checked without an account:

```sh
FUSEDOCK_LIVE_PKU=1 go test -run '^TestLivePKULoginPage$' -v ./internal/providers/pku
```

## Why this shape

Cloud providers disagree on object identity, modification versions, pagination, authentication, and byte-range support. FuseDock keeps those differences inside a provider and requires the adapter to return stable object IDs and versions. The mount layer never reconstructs a vendor path or trusts a vendor name as an identity.

The request path is:

```text
Linux FUSE / macOS FSKit / WinFsp / GUI / CLI
                 |
             vfs cache
          |
   provider.Session
          |
  local / PKU / future providers
```

The metadata cache coalesces concurrent listings and stats. Version-bound readers share immutable blocks through the daemon. PKU binds each download URL renewal to the selected revision. Baidu uses ordinary current-content reads until its declared-digest mutation and concurrency tests pass. Whole-file verification is an optional capability path, with a size limit. Metadata TTL and persistence are mount settings; their defaults still need native workload measurements.

Read [the architecture](docs/architecture.md) for invariants, platform boundaries, and the planned write path. Read [the provider guide](docs/provider-guide.md) to add a backend and [the provider matrix](docs/providers.md) for the supported and planned services.

The prioritized backlog is in [TODO.md](TODO.md). The persistent cache contract is in [docs/persistent-block-cache.md](docs/persistent-block-cache.md). Its implementation and remaining acceptance work are recorded in [foundation status](docs/foundation-status.md).

The account, object, surface-path, inode, and cache identity rules are specified in [docs/namespace-contract.md](docs/namespace-contract.md).

The earlier external-review snapshot is archived in [docs/design-review.md](docs/design-review.md). Current decisions and acceptance criteria are in [TODO.md](TODO.md).

The review decisions, updated on 2026-10-28, are in [docs/design-review-response.md](docs/design-review-response.md). The WebDAV implementation has been deleted; its rewrite and existing-account migration are the lowest-priority features.

## Commands

```text
fusedock providers
fusedock account add [flags]
fusedock account ls [--json]
fusedock account default <name>
fusedock account rename <old-name> <new-name>
fusedock account remove <name>
fusedock broker start --account <name> --config <path>
fusedock ls [flags] [provider]
fusedock mount [flags] [provider] <mountpoint>
fusedock daemon start
fusedock daemon mount --id <mount> --account <name> --mountpoint <path> --scope /
fusedock daemon status
fusedock daemon ls --id <mount> --path /
fusedock daemon cat --id <mount> --path /file --offset 0 --length 4096
fusedock daemon refresh --account <name>
fusedock daemon cache-info
fusedock daemon cache-verify
fusedock daemon cache-prune --bytes <budget>
fusedock daemon cache-clear --account <name>
fusedock daemon unmount --id <mount>
fusedock daemon stop
fusedock version
```

Persistent retention requires an account with a stable AccountID. New account bindings include one. Older saved accounts can mount with `--persistent-cache=false`; their migration remains separate backlog work. Requesting persistence for a legacy account returns an explicit error before credential access.

Baidu application credentials can be imported from an owner-only bootstrap file:

```sh
./scripts/setup-baidu-app.sh
bin/fusedock account add \
  --credential-file "$HOME/.config/fusedock/baidu-app.credentials" \
  --name baidu --default
bin/fusedock ls --account baidu
```

See the [Baidu guide](docs/baidu.md) for the device authorization flow and mount commands.

Provider-specific configuration uses repeatable `--option key=value`. Credentials use `--credential-file PATH`, `--credential key=value`, or `--credential-env key=ENV`. Credential files must be regular files owned by the current user with no group or world permissions. Secrets are cleared from the in-memory map after the provider opens.

## macOS FSKit

FuseDock uses Apple's native FSKit extension on macOS 26 and newer. On macOS 26, `OpenCloseOperations` manages readers and each kernel item retains its observed file version; unmount and mount again to refresh remotely changed files. On macOS 27, an Xcode 27 build selects `DataCacheHandler` for cache negotiation and `setCacheState` for explicit data invalidation. On macOS 27.2, native snapshot, same-size change and rename tests pass in subdirectories; the root-directory reopen test fails because its cache invalidation returns an error. Remount to refresh remotely changed files directly under the mounted root. See [the acceptance record](docs/foundation-status.md). The Swift extension owns the FSKit object model; a Go `c-archive` provides the same provider and VFS core used by Linux and the CLI.

```sh
DEVELOPMENT_TEAM=YOUR_TEAM_ID make macos-install
open "$HOME/Applications/FuseDock.app"
```

`macos-install` also unregisters stale FSKit extensions created by Xcode's temporary build directories, so only the installed FuseDock appears under File System Extensions.

Enable the module using **By Category → File System Extensions** in System Settings. The **By Application** view has a reported FSKit toggle bug. If the category view remains unresponsive after several development installs, the explicit development helper is `make macos-enable`. It backs up and repairs the per-user FSKit state before restarting the FSKit agent.

Enable FuseDock under **System Settings → General → Login Items & Extensions → File System Extensions**. Start the broker in one terminal:

```sh
bin/fusedock broker start --account pku \
  --config "$HOME/.config/fusedock/pku.broker.credentials"
```

Mount from another terminal with the generated one-use capability file:

```sh
mkdir -p "$HOME/FuseDockPKU"
mount -t fusedock \
  "$HOME/.config/fusedock/pku.broker.credentials" \
  "$HOME/FuseDockPKU"
umount "$HOME/FuseDockPKU"
```

Stop the mount before pressing Ctrl-C in the broker terminal. See the [macOS FSKit guide](docs/macos-fskit.md) and [broker security review](docs/security.md) for the trust boundary and residual risks.

## Development

```sh
make test
make check
make build
```

`make check` runs formatting checks, `go vet`, the race-enabled test suite, and cross-compiles Linux, macOS, and Windows binaries. New providers can use `pkg/providertest` for shared namespace and range-read conformance checks. Native FUSE and FSKit mount tests are separate because they require a filesystem driver, signing, and a real mount point.

`make test-core`, `make test-gofuse`, `make test-linux`, `make test-macos`, and `make test-windows` are separate test entries. The three native entries opt into real mounts and require their respective host driver. GitHub Actions has separate core, go-fuse, Linux FUSE, Windows WinFsp, Xcode 26/27, and cross-build jobs. The FSKit native job requires a configured signed-extension host. On any network-enabled machine, `make test-live-pku` checks the public PKU login page without credentials.

The PKU provider's IAAA/AnyShare flow is derived from the Apache-2.0 `unofficialpku/disk` project. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Scope

FuseDock aims to provide a shared local representation of cloud files across systems. It does not currently provide bidirectional offline synchronization, collaborative editing, remote locking, or conflict merging. A future write path will publish local writes explicitly and use provider versions to reject stale overwrites.
