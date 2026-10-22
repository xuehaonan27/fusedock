# FuseDock

FuseDock turns cloud storage into local file systems through a provider-neutral core. A provider implements a small read contract once; the same provider can then serve CLI inspection, FUSE mounts, and future WinFsp, GUI, and mobile surfaces.

The repository currently contains the first vertical slice: a read-only Linux FUSE driver, a native macOS FSKit extension, a local-directory reference provider, and a PKU NetDisk provider for IAAA and AnyShare. Sessions are opened for one run and are not persisted yet.

## Current status

| Area | Status |
| --- | --- |
| Provider contract | Versioned read-only contract in `pkg/provider` |
| Local provider | Implemented and tested |
| PKU NetDisk | Implemented for read-only IAAA login, namespace traversal, and range reads |
| Linux FUSE | Implemented with go-fuse; build tested, native mount requires the FUSE 3 package |
| macOS FSKit | Implemented as a native user-space extension; no macFUSE or kernel extension |
| Windows | CLI and provider layer build; WinFsp mount adapter is next |
| Baidu NetDisk, Google Drive | Not implemented yet; both fit the same provider contract |
| Writable mounts | Not implemented; the capability and conflict model is defined in the architecture |
| Multi-account namespace | Not implemented; a session currently represents one account |

This release should be treated as an architecture baseline and read-only preview, not a complete replacement for each cloud vendor's desktop client.

## Quick start

Go 1.24 or newer is required.

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

Press Ctrl-C in the mount terminal to unmount. The Linux FUSE 3 package must provide `/dev/fuse` and `fusermount3`.

Inspect provider contents without mounting:

```sh
bin/fusedock ls local --option root="$HOME/Documents"
bin/fusedock ls local --option root="$HOME/Documents" --json
```

## Mount PKU NetDisk

The PKU provider signs in through IAAA and exposes the account's accessible AnyShare document libraries at the mount root. It does not persist a session; the credential file remains under your control.

Interactive password entry:

```sh
mkdir -p /tmp/fusedock-pku
bin/fusedock mount pku /tmp/fusedock-pku --username YOUR-CAMPUS-ID
```

For repeatable local use, create an owner-only credential file with the helper script. The password prompt stays out of shell history:

```sh
./scripts/setup-pku-credentials.sh
bin/fusedock ls pku --credential-file "$HOME/.config/fusedock/pku.credentials"
```

The same file works with the macOS FSKit extension. Alternatively, pass a credential from an environment variable:

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

The metadata cache coalesces concurrent listings and stats. Each open file handle reads in 4 MiB blocks, while PKU obtains a fresh signed range for each provider read. Providers receive the caller's context on every `ReadAt`, so cancellation does not depend on a request that has already returned.

Read [the architecture](docs/architecture.md) for invariants, platform boundaries, and the planned write path. Read [the provider guide](docs/provider-guide.md) to add a backend and [the provider matrix](docs/providers.md) for the supported and planned services.

## Commands

```text
fusedock providers
fusedock ls [flags] <provider>
fusedock mount [flags] <provider> <mountpoint>
fusedock version
```

Provider-specific configuration uses repeatable `--option key=value`. Credentials use `--credential-file PATH`, `--credential key=value`, or `--credential-env key=ENV`. Credential files must be regular files owned by the current user with no group or world permissions. Secrets are cleared from the in-memory map after the provider opens.

## macOS FSKit

FuseDock uses Apple's native FSKit extension on macOS, with no macFUSE or kernel extension. The Swift extension owns the FSKit object model; a Go `c-archive` provides the same provider and VFS core used by Linux and the CLI.

```sh
DEVELOPMENT_TEAM=YOUR_TEAM_ID make macos-install
open "$HOME/Applications/FuseDock.app"
```

`macos-install` also unregisters stale FSKit extensions created by Xcode's temporary build directories, so only the installed FuseDock appears under File System Extensions.

If macOS leaves that one toggle unresponsive after several development installs, quit System Settings and rerun `make macos-enable`. It backs up and repairs the per-user FSKit state before restarting the FSKit agent.

Enable FuseDock under **System Settings → General → Login Items & Extensions → File System Extensions**, then mount:

```sh
mkdir -p "$HOME/FuseDockPKU"
mount -t fusedock \
  "$HOME/.config/fusedock/pku.credentials" \
  "$HOME/FuseDockPKU"
umount "$HOME/FuseDockPKU"
```

See the [macOS FSKit guide](docs/macos-fskit.md) for the signed build, security-scoped credential handoff, and current limitations.

## Development

```sh
make test
make check
make build
```

`make check` runs formatting checks, `go vet`, the race-enabled test suite, and cross-compiles Linux, macOS, and Windows binaries. New providers can use `pkg/providertest` for shared namespace and range-read conformance checks. Native FUSE and FSKit mount tests are separate because they require a filesystem driver, signing, and a real mount point.

On a FUSE-enabled Linux host, `make test-fuse` mounts a local fixture and reads it through the real driver. On any network-enabled machine, `make test-live-pku` checks the public PKU login page without credentials.

The PKU provider's IAAA/AnyShare flow is derived from the Apache-2.0 `unofficialpku/disk` project. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Scope

FuseDock aims to provide a shared local representation of cloud files across systems. It does not currently provide bidirectional offline synchronization, collaborative editing, remote locking, or conflict merging. A future write path will publish local writes explicitly and use provider versions to reject stale overwrites.
