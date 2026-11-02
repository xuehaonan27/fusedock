# Mount Baidu NetDisk (百度网盘)

FuseDock exposes Baidu NetDisk as a read-only filesystem using the official OAuth device flow and XPAN APIs. A saved account works with macOS FSKit, Linux FUSE and Windows WinFsp.

If you have already authorized an account and `fusedock ls --account baidu` succeeds, go directly to [macOS mounting](#mount-on-macos), [Linux mounting](#mount-on-linux), or [Windows mounting](#mount-on-windows). The examples use `baidu` as the saved account name; substitute your existing alias if it differs.

## Prerequisites

Run the commands from the repository root. Build the CLI with Go 1.25 or newer:

```sh
go build -o bin/fusedock ./cmd/fusedock
```

On Windows, use `go build -o .\bin\fusedock.exe ./cmd/fusedock` in PowerShell.

| Platform | Mount requirement |
| --- | --- |
| macOS | macOS 26 or newer, with the signed FuseDock app installed and its FSKit extension enabled; see the [FSKit setup guide](macos-fskit.md). Use an Xcode 27 build on macOS 27 for automatic refresh. |
| Linux | FUSE 3, `/dev/fuse`, `fusermount3`, and an empty mount directory owned by your user. |
| Windows | WinFsp and an unused drive letter, or a directory path whose parent exists and whose final directory does not yet exist. |

Use the CLI, daemon and extension from the same revision. On macOS, enable FuseDock through **System Settings → General → Login Items & Extensions → By Category → File System Extensions**.

## Application requirements

Create or select your application in the [Baidu NetDisk developer console](https://pan.baidu.com/union/console/applist). It needs access to the `basic,netdisk` OAuth scope used by FuseDock. The device-code flow uses a verification URL and a user code, without a local callback server; see Baidu's [device authorization API](https://github.com/baidu-netdisk/mcp/blob/main/src/baidu-netdisk/openapi_client/api/auth_api.py).

| Application field | FuseDock credential | Purpose |
| --- | --- | --- |
| AppKey / API Key (`client_id`) | `app_key` | Identifies your application. |
| SecretKey (`client_secret`) | `secret_key` | Authenticates the application during token exchange and refresh. |

Your Baidu account login happens on Baidu's browser page. Application keys and OAuth tokens are saved in the host credential store: macOS Keychain, Linux Secret Service, or Windows Credential Manager.

## Authorize an account

The interactive command works without a bootstrap file:

```sh
bin/fusedock account add --provider baidu --name baidu
```

In Windows PowerShell:

```powershell
.\bin\fusedock.exe account add --provider baidu --name baidu
```

1. Enter the AppKey and SecretKey when prompted. SecretKey entry is hidden in an interactive terminal.
2. The command prints a verification URL and user code, then opens the browser. If the browser does not open, visit the printed URL yourself.
3. Sign in to the Baidu account you want to mount, enter the code, and approve access. Keep the command running until authorization completes.
4. Verify the saved account:

```sh
bin/fusedock account ls
bin/fusedock ls --account baidu
```

On Windows, use `.\bin\fusedock.exe` for these commands. An existing default account remains selected; all examples explicitly use `--account baidu`. To make Baidu the default, run `bin/fusedock account default baidu`.

### Import an application bootstrap file

On macOS or Linux, the existing helper can collect the same keys and write an owner-only bootstrap file:

```sh
./scripts/setup-baidu-app.sh
```

The helper writes:

```text
~/.config/fusedock/baidu-app.credentials
```

with mode `0600`. You can also create that file manually; it must contain:

```text
provider=baidu
app_key=YOUR-APPKEY
secret_key=YOUR-SECRETKEY
```

Import the file and complete the same browser authorization flow:

```sh
bin/fusedock account add \
  --credential-file "$HOME/.config/fusedock/baidu-app.credentials" \
  --name baidu
```

After `fusedock account ls` and `fusedock ls --account baidu` succeed, remove the bootstrap file:

```sh
rm "$HOME/.config/fusedock/baidu-app.credentials"
```

## Browse without mounting

```sh
bin/fusedock ls --account baidu
bin/fusedock ls --account baidu --path /Documents
```

Replace `/Documents` with a directory that exists in your account. The commands in the following sections use that same saved login.

## Mount on macOS

### Background mount with the daemon

After installing and enabling the FSKit extension, create an empty directory and mount the saved account:

```sh
mkdir -p "$HOME/FuseDockBaidu"
bin/fusedock daemon start
bin/fusedock daemon mount --id baidu --account baidu \
  --mountpoint "$HOME/FuseDockBaidu" --scope / \
  --persistent-cache=false
bin/fusedock daemon status
```

`--account baidu` selects the saved login; `--id baidu` names this mount for subsequent commands. Mounting is asynchronous: rerun `daemon status` until its state is `mounted`. If it reports `failed`, read that mount's `error` field.

Then verify the native filesystem and open it in Finder:

```sh
ls "$HOME/FuseDockBaidu"
open "$HOME/FuseDockBaidu"
```

You can close the terminal while the daemon continues serving the mount. To inspect the provider through the existing daemon session, use `bin/fusedock daemon ls --id baidu --path /`.

Unmount after closing applications that are using the files:

```sh
bin/fusedock daemon unmount --id baidu
```

`bin/fusedock daemon stop` stops the whole daemon, including other mounts. `daemon start` starts a background process; login startup is a separate `daemon install` operation described in the [FSKit guide](macos-fskit.md#daemon-mounting).

### Foreground mount

For a mount that lasts while a terminal stays open:

```sh
mkdir -p "$HOME/FuseDockBaidu"
bin/fusedock mount --account baidu "$HOME/FuseDockBaidu"
```

Browse the directory from Finder or another terminal. Press Ctrl-C in the mount terminal to unmount. The command starts its FSKit broker automatically. Use this workflow or the daemon workflow for a given mountpoint.

## Mount on Linux

With FUSE 3 available, mount in a directory your user owns:

```sh
mkdir -p "$HOME/FuseDockBaidu"
bin/fusedock mount --account baidu "$HOME/FuseDockBaidu"
```

In another terminal:

```sh
ls "$HOME/FuseDockBaidu"
```

Press Ctrl-C in the mount terminal to unmount. For a background mount, use the daemon commands from the [macOS example](#background-mount-with-the-daemon), including `--persistent-cache=false`; browse with your file manager or `ls` instead of the macOS `open` command. The platform adapter selects Linux FUSE automatically.

## Mount on Windows

After installing WinFsp and saving the Baidu account, choose an unused drive letter. This PowerShell example uses `B:`:

```powershell
.\bin\fusedock.exe mount --account baidu 'B:'
```

Keep that terminal open. From another PowerShell window, inspect the drive and open Explorer:

```powershell
Get-ChildItem 'B:\'
explorer.exe 'B:\'
```

Press Ctrl-C in the mount terminal to unmount. For a background mount, use these commands instead:

```powershell
.\bin\fusedock.exe daemon start
.\bin\fusedock.exe daemon mount --id baidu --account baidu --mountpoint 'B:' --scope / --persistent-cache=false
.\bin\fusedock.exe daemon status
```

Wait for state `mounted` before opening `B:`. Unmount with:

```powershell
.\bin\fusedock.exe daemon unmount --id baidu
```

For a directory mount, pass a path such as `C:\Mounts\Baidu` after creating only `C:\Mounts`. WinFsp creates the final mount directory itself.

## Mount a remote subdirectory

The daemon's `--scope` selects which remote directory appears as the filesystem root. To change an existing `baidu` daemon mount to `/Documents`, unmount it first, then mount with the new scope:

```sh
bin/fusedock daemon unmount --id baidu
bin/fusedock daemon mount --id baidu --account baidu \
  --mountpoint "$HOME/FuseDockBaidu" --scope /Documents \
  --persistent-cache=false
bin/fusedock daemon status
```

Use a directory that exists in your account, and quote paths containing spaces. On Windows, use the PowerShell executable path and your chosen drive letter. Mount scope is independent of the OAuth permission scope granted during authorization.

## Refresh and caching

File reads fetch the missing byte ranges requested by the application; a small first read no longer downloads a whole 4 MiB cache block. Sequential reads start asynchronous read-ahead at 64 KiB and grow it up to 4 MiB. Bytes from an in-flight Baidu response become available in 32 KiB increments, so a small foreground read can finish before that response ends. A seek or closing the read handle cancels unused prefetch. The read handle reuses its download link and refreshes a reused link once after HTTP 401, 403, 404 or 410.

This reduces first-read transfer and latency. It also makes more range requests than one large synchronous download; the [controlled measurements](foundation-status.md#read-delivery-measurements) report both latency and request count. Image decoders and the OS can still request more than the file header. Baidu data is cached in memory for the open read handle and released at close; native OS buffering may also serve repeated reads.

On macOS 27 with the current CLI/broker and app, remote directory changes refresh automatically through root namespace publication and child-directory cache invalidation. The daemon's metadata TTL defaults to two seconds. On macOS 26, refresh the mounted view by unmounting, refreshing daemon metadata, and mounting again:

```sh
bin/fusedock daemon unmount --id baidu
bin/fusedock daemon refresh --account baidu
bin/fusedock daemon mount --id baidu --account baidu \
  --mountpoint "$HOME/FuseDockBaidu" --scope / \
  --persistent-cache=false
```

Preserve your chosen scope when remounting. Linux and Windows use their native adapter refresh behavior. See [platform acceptance](foundation-status.md) for the tested filesystem contracts.

Baidu currently provides current-content reads. Its download URLs have not been qualified for revision-bound reads, and declared-digest persistent caching remains disabled pending provider tests. The examples use `--persistent-cache=false`, which also lets older saved accounts without an AccountID mount through the daemon. Native buffering and metadata caching remain in use. The snapshot guarantees verified with revision-capable test providers are separate from Baidu's current-content capability.

## Manual FSKit broker workflow

For a manually managed macOS mount, start the broker in one terminal:

```sh
bin/fusedock broker start --account baidu \
  --config "$HOME/.config/fusedock/baidu.broker.credentials"
```

In another terminal, mount its one-use capability file:

```sh
mkdir -p "$HOME/FuseDockBaidu"
mount -t fusedock \
  "$HOME/.config/fusedock/baidu.broker.credentials" \
  "$HOME/FuseDockBaidu"
ls "$HOME/FuseDockBaidu"
open "$HOME/FuseDockBaidu"
```

The capability file is consumed and removed during mounting. Keep the broker process running. When finished, run `umount "$HOME/FuseDockBaidu"`, then Ctrl-C in the broker terminal. Start a new broker to obtain a fresh capability before mounting again.

`baidu-app.credentials` imports application keys during account setup. `baidu.broker.credentials` is a temporary mount capability generated by the broker. The managed `mount` and `daemon mount` commands handle the latter automatically.

## Troubleshooting

| Symptom | Action |
| --- | --- |
| Saved account `baidu` is not found | Run `fusedock account ls` and use the existing alias, or complete account authorization. |
| Browser does not open, or the device code expires | Open the URL printed by `account add`. If the command reports expiration, rerun it to obtain a fresh code. |
| `persistent caching requires a stable AccountID` | Add `--persistent-cache=false` to `daemon mount`, as shown above. |
| Daemon state remains `mounting` or becomes `failed` | Check `daemon status` and its `error` field. Verify the platform driver/extension and mountpoint requirements. |
| FSKit toggle does not respond | Use **By Category → File System Extensions**; see the [FSKit setup guide](macos-fskit.md). |
| Mount ID or mountpoint is already active | Use the existing mount or unmount it before starting another workflow at that path. |
| The daemon still uses an older CLI build | After finishing current file operations, run `daemon stop` and `daemon start` with the updated executable. Check `daemon status` for restored mounts before creating a mount again. |
| Broker capability file disappeared | It is removed after the extension claims it. A new manual mount needs a newly started broker. |
| Files can be listed but a read reports unsupported Range | Baidu did not honor the requested byte range. Listing access and ranged download support are checked separately; keep the reported error when diagnosing that file. |

## Protocol behavior

- Directory listing uses `GET https://pan.baidu.com/rest/2.0/xpan/file?method=list`, with `start`/`limit` pagination and `fs_id` as the stable object identity.
- Download authorization uses `GET /rest/2.0/xpan/multimedia?method=filemetas&fsids=[ID]&dlink=1`.
- The returned `dlink` is used with `access_token` and `User-Agent: pan.baidu.com`. Download redirects are limited to Baidu-owned HTTPS hosts (or loopback in tests).
- Reads request bounded HTTP `Range` slices and validate `Content-Range` and the selected file size. If Baidu ignores a non-zero range, the provider reports `ENOTSUP` rather than returning wrong bytes.
- Access tokens refresh through `https://openapi.baidu.com/oauth/2.0/token` with the application credentials. Rotated access and refresh tokens are atomically saved in opaque provider state.
- Token errors and permission errors are mapped to provider errors; tokens, application secrets, signed URLs, and response bodies do not appear in returned error messages.

## Verification

```sh
go test -race ./internal/providers/baidu
```

The fixture covers device authorization, authorization polling, openid lookup, listing, stable IDs, download headers, exact range reads, refresh-token rotation, and rejection of untrusted endpoints. Live acceptance requires the Baidu application and a real account.
