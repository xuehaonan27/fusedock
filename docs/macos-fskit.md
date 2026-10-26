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

- macOS 26.0 or newer for the `FSPathURLResource` interface used by this build. FSKit itself begins at macOS 15.4.
- Xcode with the macOS SDK and a valid Apple Development signing identity.
- A development team with the FSKit file-system-extension entitlement. Xcode obtains the profile automatically for normal Apple Developer accounts.

No macFUSE installation is needed.

## Build and install

```sh
make test
DEVELOPMENT_TEAM=YOUR_TEAM_ID make macos-install
open "$HOME/Applications/FuseDock.app"
```

The build phase compiles the Go bridge as a Darwin `c-archive`, signs the application and extension, and embeds the FSKit module entitlement. `make macos-fskit` uses automatic provisioning; it may update the developer profile in Xcode. `make macos-install` copies the result to `~/Applications/FuseDock.app` and removes stale PluginKit registrations left by Xcode's DerivedData builds. Rerun it after rebuilding so the system Settings list contains one FuseDock entry.

In the app, click **Open Login Items & Extensions**, then enable **FuseDock** under **File System Extensions**. The extension can be disabled later from the same panel. This approval is a macOS user-security step and cannot be safely bypassed by the build system.

If the toggle appears unresponsive after repeated local development builds, quit and reopen System Settings first. For a development install, `make macos-enable` repairs stale test module IDs in the user-level FSKit state, backs up the previous state under `~/.config/fusedock`, and restarts `fskit_agent`. Release builds should use the System Settings toggle and should not ship this repair path as a silent installation step.

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

This is the current implementation, not the final lifecycle model. If macOS restarts the extension, its in-memory capability is gone and the one-use mount resource cannot be claimed again. The planned launchd-managed daemon and peer-authenticated UDS/XPC channel will let the extension reconnect without the CLI being present.

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
- The broker is a foreground development process; launchd supervision and peer-authenticated XPC/Unix-socket transport are pending.
- The legacy direct credential-file path remains as a fallback, but it is read-only and does not persist a refresh session.
- The extension requires local signing and the one-time Filesystem Extension toggle.
- The current implementation is a development build; distribution needs an app bundle, notarization, and a release workflow.
