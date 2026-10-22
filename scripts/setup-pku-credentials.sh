#!/bin/bash
set -euo pipefail

config_dir=${FUSEDOCK_CONFIG_DIR:-"$HOME/.config/fusedock"}
config_file=${1:-"$config_dir/pku.credentials"}

mkdir -p "$config_dir"
chmod 700 "$config_dir"

if [[ -e "$config_file" && ${2:-} != "--replace" ]]; then
  printf 'Refusing to overwrite %s. Pass --replace to replace it.\n' "$config_file" >&2
  exit 1
fi

printf 'PKU campus username: '
read -r pku_username
printf 'PKU password: '
read -r -s pku_password
printf '\n'

if [[ -z "$pku_username" || -z "$pku_password" ]]; then
  printf 'Username and password must not be empty.\n' >&2
  exit 1
fi

umask 077
tmp_file=$(mktemp "$config_dir/.pku-credentials.XXXXXX")
trap 'unlink "$tmp_file" 2>/dev/null || true' EXIT
printf 'provider=pku\nusername=%s\npassword=%s\n' \
  "$pku_username" "$pku_password" > "$tmp_file"
chmod 600 "$tmp_file"
mv "$tmp_file" "$config_file"
trap - EXIT
unset pku_username pku_password

printf 'Credential file created: %s\n' "$config_file"
printf 'Check access with:\n  bin/fusedock ls pku --credential-file %q\n' "$config_file"
