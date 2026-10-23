# Accounts and sessions

FuseDock keeps account metadata separate from secrets. The metadata file contains provider IDs, usernames, endpoints, and the default account. Passwords and opaque provider session state live in the host credential store:

| Platform | Credential backend |
| --- | --- |
| macOS | Login Keychain through `security` |
| Linux | Secret Service through the desktop keyring |
| Windows | Windows Credential Manager |

The account metadata file is owner-only:

```text
macOS:   ~/Library/Application Support/fusedock/accounts.json
Linux:   ~/.config/fusedock/accounts.json
Windows: %AppData%\fusedock\accounts.json
```

Secret items use the service name `dev.fusedock.accounts` and the local account name as their key. The payload is versioned JSON containing provider-defined secret keys, such as a password or OAuth token, plus an opaque provider state value. FuseDock rejects account-store files with group or world permissions on Unix.

## Add an account

The recommended command prompts for the password without putting it in shell history or process arguments:

```sh
bin/fusedock account add \
  --provider pku \
  --username YOUR-CAMPUS-ID \
  --name pku \
  --default
```

Existing one-shot credential files can bootstrap the account store:

```sh
bin/fusedock account add \
  --credential-file "$HOME/.config/fusedock/pku.credentials" \
  --name pku \
  --default
```

After verifying `fusedock ls --account pku`, the bootstrap file can be removed. FSKit uses the account broker rather than reading the bootstrap password file. Avoid `--credential key=value` for a long-lived password because command-line arguments can be observed by other processes.

```sh
bin/fusedock account ls
bin/fusedock account ls --json
bin/fusedock account default pku
bin/fusedock account remove pku
```

## Use an account

```sh
bin/fusedock ls --account pku
bin/fusedock ls --account pku --path 'My library/Reports'
```

On Linux, the same account can be mounted:

```sh
bin/fusedock mount --account pku /mnt/pku
```

Explicit `--credential`, `--credential-env`, `--credential-file`, and `--username` inputs cannot be mixed with `--account`. A `--option` can override non-secret metadata for one run; the saved account is not mutated.

## Session lifecycle

PKU persists its access and renewable refresh cookies as opaque provider state. On the next command or mount:

1. The account store loads the saved password and state.
2. The PKU provider binds the state to the saved endpoint, IAAA endpoint, and username.
3. A forced refresh validates the state and rotates its token.
4. The new state atomically replaces the old Keychain/Secret Service value.

Expired state falls back to password login. Network failures do not silently fall back, because a password login would hit the same transport failure and could hide a credential problem. The account-store lock covers refresh and state writes across processes.

The metadata file stores no secret. The OS credential store may still ask the user to unlock its keychain or desktop session; FuseDock cannot bypass that.

## FSKit broker

The macOS FSKit extension runs in an App Sandbox and cannot execute `/usr/bin/security` like the CLI. A direct `SecItemCopyMatching` of a login-keychain item also requires an ACL that would need one interactive approval at item creation. The broker avoids both problems:

```sh
bin/fusedock broker start --account pku \
  --config "$HOME/.config/fusedock/pku.broker.credentials"
```

The broker opens the saved account in the CLI process and serves only read-only object metadata and byte ranges. It writes an owner-only, one-use bootstrap capability. On mount, the extension exchanges it for an in-memory session capability; account passwords and refresh tokens never enter the extension. The bootstrap file is removed immediately after the claim and again when the broker exits. Keep the broker running while the FSKit mount is active.

The [security review](security.md) covers loopback binding, Origin/Host checks, proxy/redirect rejection, request limits, replay behavior, and residual same-user risk. The direct credential-file path remains as a development fallback, but it is no longer the recommended FSKit flow.

## Tests

The account tests use an in-memory credential backend for deterministic coverage. They check separate metadata/secrets, default selection, removal, state binding, and file permissions. Broker tests cover one-use claims, bootstrap rejection on data routes, Origin/Host checks, read limits, loopback-only endpoints, and capability cleanup. Network and OS credential integrations remain opt-in because they involve real accounts and host UI.

## Upstream review

The account/session implementation in `unofficialpku/disk` was reviewed before implementing this layer. Its store and session connector are mature in PKU session restoration and cross-process refresh coordination, but its account JSON contains the plaintext password and refresh session. Reusing that store directly would undo the credential-store boundary and make the sandboxed extension path harder to secure. FuseDock therefore keeps the already attributed PKU protocol code and implements metadata/secret separation, system credential storage, one-use broker capabilities, and cross-process locking independently. General locking and atomic-file ideas from upstream are compatible with this implementation, but no plaintext account-store code was copied.
