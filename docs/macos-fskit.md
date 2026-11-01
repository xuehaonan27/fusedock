# Native macOS FSKit backend

FuseDock's macOS backend uses Apple's FSKit extension framework. It does not require macFUSE, a kernel extension, a security-setting downgrade, or a reboot.

## Architecture

```text
mount -t fusedock <broker-capability-file> <mountpoint>
                    |
             FSKit app extension (Swift)
                    |
             C ABI / static Go archive
                    |
          broker provider over loopback (Go)
                    |
   account broker + credential store (Go)
                    |
          PKU / Google Drive / Baidu / ...
```

The Swift layer owns `FSUnaryFileSystem`, `FSVolume`, `FSItem`, and the FSKit callbacks. The Go static archive owns authentication, providers, metadata caching, and byte-range reads. The two layers communicate through the small API in `macos/bridge/main.go`; user credentials never cross the C boundary, only object IDs and byte ranges do.

The source path passed to `mount` is a security-scoped capability file handled by FSKit. The extension claims a one-use bootstrap capability and then performs read-only provider operations against the broker. Account passwords and refresh tokens remain in the broker process.

## Requirements

- macOS 26 or newer. Path URL resources used for mounting require macOS 26.
- Xcode 26 or newer and a valid Apple Development signing identity. Xcode 26 builds the compatibility implementation. Xcode 27 builds both implementations and selects one at runtime.
- A development team with the FSKit file-system-extension entitlement. Xcode obtains the profile automatically for normal Apple Developer accounts.

No macFUSE installation is needed.

## OS behavior

| Build and host | Lifecycle | Refresh behavior |
| --- | --- | --- |
| Xcode 26 build on macOS 26+ | `OpenCloseOperations` | Remount to refresh remotely changed files |
| Xcode 27+ build on macOS 26 | `OpenCloseOperations` | Remount to refresh remotely changed files |
| Xcode 27+ build on macOS 27+ | `Handler` / `ReadWriteHandler` / `DataCacheHandler` | Subdirectory snapshot contract passed on 27.2; root-level remote changes require remount because root cache invalidation fails |

Both paths use the same broker, provider contracts, namespace and block cache. They use normal kernel buffering. The compatibility path pins a file's observed metadata and content version for its `FSItem` lifetime. For providers with revision reads, reopening that item reads the same revision. Closing the last reader releases the provider handle; reclaim releases the item. A provider that cannot reopen the observed revision reports `ESTALE` when the newly opened metadata differs. The provider's read capabilities still determine whether its bytes have an immutable snapshot guarantee.

The item lifetime is controlled by the kernel. This is not a mount-wide historical snapshot of the remote directory tree: new lookups can observe new objects. Remounting is the supported way to refresh the whole mounted view. `daemon refresh` clears daemon metadata; on macOS 26 it cannot invalidate kernel-cached files. Finder previews and file reads use this same compatibility behavior.

`FuseDockCompatibilityFSVolume.swift` and `FuseDockCachedFSVolume.swift` own the two lifecycles. The SDK condition in `FuseDock.xcconfig` excludes unavailable symbols from Xcode 26 compilation; `#available(macOS 27, *)` selects the runtime implementation in a newer build. The app displays the applicable refresh instruction.

## Build and install

```sh
make test
DEVELOPMENT_TEAM=YOUR_TEAM_ID make macos-install
open "$HOME/Applications/FuseDock.app"
```

The build phase compiles the Go bridge as a Darwin `c-archive`, signs the application and extension, and embeds the FSKit module entitlement. `make macos-fskit` uses automatic provisioning; it may update the developer profile in Xcode. `make macos-install` copies the result to `~/Applications/FuseDock.app` and removes stale PluginKit registrations left by Xcode's DerivedData builds. Rerun it after rebuilding so the system Settings list contains one FuseDock entry.

In the app, click **Open File System Extensions**, switch the extension list to **By Category**, then open **File System Extensions** and enable **FuseDock**. The extension can be disabled later from the same panel. This approval is a macOS user-security step and cannot be safely bypassed by the build system.

If the toggle ignores clicks in **By Application**, use **By Category → File System Extensions**. These views use different Apple components; the application view has a reported FSKit authorization bug ([maintainer investigation](https://github.com/macfuse/macfuse/issues/1194)). If the category view is also unresponsive after repeated local development builds, quit and reopen System Settings first. For a development install, `make macos-enable` repairs stale test module IDs in the user-level FSKit state, backs up the previous state under `~/.config/fusedock`, and restarts `fskit_agent`. Release builds should use the System Settings toggle and should not ship this repair path as a silent installation step.

## CI builds when local Xcode is older

The CI `macos-build` jobs retain unsigned app bundles for seven days as `FuseDock-unsigned-macos-26` and `FuseDock-unsigned-xcode-27`. The Xcode 27 artifact contains both implementations. Upgrading macOS does not change the selected local Xcode or the installed app binary.

For local acceptance, download the artifact from the desired commit, sign its nested libraries, extension and host app with the local Apple Development identity, and embed the matching FSKit provisioning profile. Keep the app and extension bundle identifiers and entitlements consistent with that profile. Run `scripts/install-macos-fskit.sh <signed-app-path>` after signing. The installer verifies the source signature before replacing the installed app. Signing keys and provisioning profiles stay on the local host.

## Account broker

The recommended flow uses a saved account. Create it with the helper account command; the password prompt stays out of shell history.

```sh
bin/fusedock account add --provider pku --username YOUR-CAMPUS-ID --name pku --default
```

Start the broker and keep it running while mounted:

```sh
bin/fusedock broker start --account pku \
  --config "$HOME/.config/fusedock/pku.broker.credentials"
```

The generated file contains a loopback endpoint and a one-use capability, not a password. The extension claims it once and keeps only an in-memory session capability. The file is removed immediately after the claim. Mount from another terminal:

This is the current implementation, not the final lifecycle model. If macOS restarts the extension, its in-memory capability is gone and the one-use mount resource cannot be claimed again. The daemon now supplies service registration and same-user UDS control. The signed peer channel needed for automatic extension reconnection remains unfinished.

```sh
mkdir -p "$HOME/FuseDockPKU"
mount -t fusedock \
  "$HOME/.config/fusedock/pku.broker.credentials" \
  "$HOME/FuseDockPKU"
```

The capability file is owner-only and can be claimed once. Do not run `fusedock ls broker --credential-file` against it before mounting: that would consume the claim. If a claim is lost or the broker exits, stop it and start a new broker; the old capability is not reusable.

The mount is read-only. Writes return `EROFS`. Single-factor accounts open directly; if IAAA requires CAPTCHA or OTP while the broker has no interactive prompt context, the command reports the verification error. Run the account migration or an interactive CLI login first, then restart the broker. The broker security review is in [security.md](security.md).

## Why not macFUSE or File Provider

FSKit provides the arbitrary mount-point behavior FuseDock needs while keeping the implementation in user space. macFUSE is an additional third-party dependency. File Provider is useful for presenting cloud folders under `~/Library/CloudStorage`, but it is a different integration model and isn't a substitute for a user-selected filesystem mount point.

The NFS bridge remains a future fallback for systems where FSKit or extension deployment is unavailable. It is not part of the current build.

## Current limitations

- The FSKit extension is read-only.
- CAPTCHA/OTP prompts are not yet connected to the host application.
- The daemon has launchd service registration and same-user UDS control. Signed peer authentication and automatic extension reconnection are pending.
- On macOS 27.2, root-directory cache invalidation returns `NSOSStatusErrorDomain -536870206`, and root-level reopens can return stale content. The full native contract remains failing. The same snapshot and rename contract passes in subdirectories; see [the detailed results](foundation-status.md#macos-27-native-findings). macOS 26 has a separate native remount-refresh contract.
- The legacy direct credential-file path remains as a fallback, but it is read-only and does not persist a refresh session.
- The extension requires local signing and the one-time Filesystem Extension toggle.
- The current implementation is a development build; distribution needs an app bundle, notarization, and a release workflow.

## Daemon mounting

On macOS 26 or newer with the matching extension installed:

```sh
bin/fusedock daemon start
mkdir -p "$HOME/FuseDockPKU"
bin/fusedock daemon mount --id pku --account pku --mountpoint "$HOME/FuseDockPKU" \
  --persistent-cache=false
bin/fusedock daemon status
bin/fusedock daemon unmount --id pku
bin/fusedock daemon stop
```

The example supports existing account records without an AccountID. For a new binding with an AccountID, omit `--persistent-cache=false` to retain eligible blocks on disk.

To refresh the whole mounted view on macOS 26, or refresh root-level remote changes affected by the macOS 27.2 issue, close files opened from the mount, then run:

```sh
bin/fusedock daemon unmount --id pku
bin/fusedock daemon refresh --account pku
bin/fusedock daemon mount --id pku --account pku --mountpoint "$HOME/FuseDockPKU" \
  --persistent-cache=false
```

`daemon install` registers the user LaunchAgent for login and failure recovery. `daemon uninstall` removes the registration. These commands are explicit service operations. A normal `daemon start` does not install a login agent.

Run `make test-macos` with the matching signed extension installed. On macOS 26 it runs directory/read/reopen/EOF/read-only/remount-refresh acceptance; on macOS 27 it runs the active-invalidation snapshot contract in both the root and a subdirectory. Each suite reports the other OS contract as skipped.

See Apple's [OpenCloseOperations documentation](https://developer.apple.com/documentation/fskit/fsvolume/opencloseoperations), [DataCacheHandler documentation](https://developer.apple.com/documentation/fskit/fsvolume/datacachehandler) and [setCacheState documentation](https://developer.apple.com/documentation/fskit/fsvolume/setcachestate(for:cachemode:coherencytype:action:)). Cache state changes run outside module locks because the kernel can reenter the extension during the call.
