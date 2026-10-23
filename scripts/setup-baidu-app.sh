#!/bin/bash
set -euo pipefail

config_dir=${FUSEDOCK_CONFIG_DIR:-"$HOME/.config/fusedock"}
config_file=${1:-"$config_dir/baidu-app.credentials"}

mkdir -p "$config_dir"
chmod 700 "$config_dir"

if [[ -e "$config_file" && ${2:-} != "--replace" ]]; then
  printf 'Refusing to overwrite %s. Pass --replace to replace it.\n' "$config_file" >&2
  exit 1
fi

printf 'Baidu application AppKey: '
read -r app_key
printf 'Baidu application SecretKey: '
read -r -s secret_key
printf '\n'

if [[ -z "$app_key" || -z "$secret_key" ]]; then
  printf 'AppKey and SecretKey must not be empty.\n' >&2
  exit 1
fi

umask 077
tmp_file=$(mktemp "$config_dir/.baidu-app.XXXXXX")
trap 'unlink "$tmp_file" 2>/dev/null || true' EXIT
printf 'provider=baidu\napp_key=%s\nsecret_key=%s\n' "$app_key" "$secret_key" > "$tmp_file"
chmod 600 "$tmp_file"
mv "$tmp_file" "$config_file"
trap - EXIT
unset app_key secret_key

printf 'Baidu app bootstrap file created: %s\n' "$config_file"
printf 'Authorize the account with:\n  bin/fusedock account add --credential-file %q --name baidu --default\n' "$config_file"
