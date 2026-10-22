#!/bin/bash
set -euo pipefail

# Development fallback for a macOS System Settings toggle that remains stuck
# after reinstalling a locally signed FSKit module. This changes the same
# user-level FSKit state that the toggle writes, after making a backup.

extension_id=dev.fusedock.app.fskit
base="$HOME/Library/Group Containers/group.com.apple.fskit.settings"

if [[ ! -f "$base/enabledModules.plist" || ! -f "$base/probeOrder.plist" ]]; then
  printf 'FSKit state is unavailable at %s\n' "$base" >&2
  exit 1
fi

backup="$HOME/.config/fusedock/fskit-settings-backup-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup"
cp "$base/enabledModules.plist" "$base/probeOrder.plist" "$backup/"

EXTENSION_ID="$extension_id" FSKitSettings="$base" python3 - <<'PY'
import os
import pathlib
import plistlib

base = pathlib.Path(os.environ['FSKitSettings'])
extension_id = os.environ['EXTENSION_ID']
stale_prefixes = (
    'dev.fusedock.app42V8VXM2ND',
    'dev.fusedock.fskit',
    'dev.fusedock.app.AppEx',
    'com.example.apple-samplecode.Passthrough',
)

for name in ('enabledModules.plist', 'probeOrder.plist'):
    path = base / name
    with path.open('rb') as stream:
        values = plistlib.load(stream)
    values = [value for value in values if not value.startswith(stale_prefixes) and value != extension_id]
    if name == 'enabledModules.plist':
        values.append(extension_id)
    else:
        values.insert(0, extension_id)
    with path.open('wb') as stream:
        plistlib.dump(values, stream, fmt=plistlib.FMT_XML, sort_keys=False)
PY

plutil -lint "$base/enabledModules.plist" "$base/probeOrder.plist" >/dev/null

# fskit_agent caches enablement for the current login session.
if agent_pid=$(pgrep -x fskit_agent | head -1); then
  kill -KILL "$agent_pid" 2>/dev/null || true
  sleep 2
fi

printf 'Enabled %s for the current user.\n' "$extension_id"
printf 'Backup: %s\n' "$backup"
printf 'Quit and reopen System Settings; fskit_agent restarts on the next FSKit request.\n'
