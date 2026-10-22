# Native macOS FSKit backend

FuseDock's macOS backend uses Apple's FSKit extension framework. It does not require macFUSE, a kernel extension, a security-setting downgrade, or a reboot.

## Architecture

```text
mount -t fusedock <credential-file> <mountpoint>
                    |
             FSKit app extension (Swift)
                    |
             C ABI / static Go archive
                    |
             provider + VFS (Go)
                    |
          PKU / Google Drive / Baidu / ...
```

The Swift layer owns `FSUnaryFileSystem`, `FSVolume`, `FSItem`, and the FSKit callbacks. The Go static archive owns authentication, providers, metadata caching, and byte-range reads. The two layers communicate through the small API in `macos/bridge/main.go`; user credentials never cross the C boundary, only object IDs and byte ranges do.

The source path passed to `mount` is a security-scoped file URL handled by FSKit. FuseDock opens it, authenticates the selected provider, and uses the same `pkg/provider` contract as the CLI and Linux FUSE backend.

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

## Credential file

Create an owner-only file under the user's configuration directory with the helper script. It prompts locally and keeps the password out of shell history and command-line arguments.

```sh
./scripts/setup-pku-credentials.sh
```

The file format is:

```text
provider=pku
username=YOUR-CAMPUS-ID
password=YOUR-PASSWORD
endpoint=https://disk.pku.edu.cn
iaaa_endpoint=https://iaaa.pku.edu.cn
```

`endpoint` and `iaaa_endpoint` are optional; omit them to use the defaults. Provider options and credentials share the same file, so CLI inspection and FSKit use identical configuration:

```sh
bin/fusedock ls pku --credential-file "$HOME/.config/fusedock/pku.credentials"
```

FuseDock rejects symlinks, files owned by another user, group/world-accessible modes, duplicate keys, unknown provider keys, and files over 64 KiB. The password is cleared from the Go secret map after the provider opens. The file remains on disk for future mounts, so protect the containing directory and use full-disk encryption.

FSKit receives this file as a security-scoped path resource. The extension can read it without a broad `Downloads` or home-directory permission.

## Mount

```sh
mkdir -p "$HOME/FuseDockPKU"
mount -t fusedock \
  "$HOME/.config/fusedock/pku.credentials" \
  "$HOME/FuseDockPKU"
ls "$HOME/FuseDockPKU"
umount "$HOME/FuseDockPKU"
```

The mount is read-only. Writes return `EROFS`. Single-factor accounts open directly; if IAAA requires CAPTCHA or OTP while the FSKit extension has no prompt UI, the mount reports the verification error and the user must complete an interactive provider login path before retrying. A future account service will persist the renewable session inside an app-group container and handle prompts through the app.

## Why not macFUSE or File Provider

FSKit provides the arbitrary mount-point behavior FuseDock needs while keeping the implementation in user space. macFUSE is an additional third-party dependency. File Provider is useful for presenting cloud folders under `~/Library/CloudStorage`, but it is a different integration model and isn't a substitute for a user-selected filesystem mount point.

The NFS bridge remains a future fallback for systems where FSKit or extension deployment is unavailable. It is not part of the current build.

## Current limitations

- The FSKit extension is read-only.
- CAPTCHA/OTP prompts are not yet connected to the host application.
- Session persistence and account management inside the app are pending.
- The extension requires local signing and the one-time Filesystem Extension toggle.
- The current implementation is a development build; distribution needs an app bundle, notarization, and a release workflow.
