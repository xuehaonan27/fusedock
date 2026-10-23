# Baidu NetDisk provider

The Baidu provider is read-only. It uses the official Baidu OAuth device flow and XPAN file APIs; it does not use the OpenList token service or private download endpoints.

## Application requirements

Create a Baidu developer application and enable the `netdisk` capability for `basic,netdisk` scope. The device-code flow does not need a localhost redirect URI, so no local callback port needs to be registered with Baidu.

FuseDock needs the application's **AppKey** and **SecretKey**. These are application credentials, not the Baidu account password. They are sent only to Baidu's HTTPS OAuth endpoint and are kept in the system credential store after setup.

## Provide the application credentials locally

Run the helper in a terminal. It hides the SecretKey input and writes an owner-only bootstrap file:

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

Do not put the application keys in a command-line flag or send them in chat. The bootstrap file is only an import mechanism.

## Authorize the account

```sh
bin/fusedock account add \
  --credential-file "$HOME/.config/fusedock/baidu-app.credentials" \
  --name baidu \
  --default
```

The command starts Baidu's OAuth device flow, prints a one-time user code, and opens the official verification page. Sign in and approve `basic,netdisk` access. FuseDock then exchanges the device code for an access token and a renewable refresh token, fetches the account's stable openid, and stores the account in the system credential store.

After `fusedock account ls` and `fusedock ls --account baidu` succeed, remove the bootstrap file:

```sh
rm "$HOME/.config/fusedock/baidu-app.credentials"
```

## Browse and mount

```sh
bin/fusedock ls --account baidu
bin/fusedock ls --account baidu --path /Documents
```

Start the FSKit broker with the saved account:

```sh
bin/fusedock broker start --account baidu \
  --config "$HOME/.config/fusedock/baidu.broker.credentials"
```

Then mount the generated capability file from another terminal:

```sh
mkdir -p "$HOME/FuseDockBaidu"
mount -t fusedock \
  "$HOME/.config/fusedock/baidu.broker.credentials" \
  "$HOME/FuseDockBaidu"
umount "$HOME/FuseDockBaidu"
```

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
