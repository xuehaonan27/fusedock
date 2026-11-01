#!/bin/bash
set -euo pipefail

repo_dir=$(cd "$(dirname "$0")/.." && pwd)
source_app=${1:-"$repo_dir/macos/FSKit/DerivedData/Build/Products/Debug/FuseDock.app"}
dest_app="$HOME/Applications/FuseDock.app"
extension_id=dev.fusedock.app.fskit

if [[ ! -d "$source_app" ]]; then
  printf 'Built app not found: %s\nRun DEVELOPMENT_TEAM=YOUR_TEAM_ID make macos-fskit first.\n' "$source_app" >&2
  exit 1
fi

codesign --verify --deep --strict "$source_app"

mkdir -p "$HOME/Applications"
osascript -e 'tell application id "dev.fusedock.app" to quit' 2>/dev/null || true

# Xcode registers every DerivedData build with PluginKit. Remove stale copies
# so System Settings presents one canonical FuseDock extension.
while IFS= read -r plugin; do
  [[ -z "$plugin" || $plugin == "$dest_app/Contents/Extensions/FuseDockFSKit.appex" ]] && continue
  if [[ -f "$plugin/Contents/Info.plist" ]] && \
     [[ $(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$plugin/Contents/Info.plist" 2>/dev/null || true) == "$extension_id" ]]; then
    pluginkit -r "$plugin" 2>/dev/null || true
  fi
done < <(pluginkit -m -A -D -v -p com.apple.fskit.fsmodule 2>/dev/null | awk -F '\t' 'NF >= 4 { print $4 }')

pluginkit -r "$dest_app/Contents/Extensions/FuseDockFSKit.appex" 2>/dev/null || true
ditto "$source_app" "$dest_app"
codesign --verify --deep --strict "$dest_app"

lsregister=/System/Library/Frameworks/CoreServices.framework/Versions/Current/Frameworks/LaunchServices.framework/Versions/Current/Support/lsregister
"$lsregister" -f -R "$dest_app"
pluginkit -a "$dest_app/Contents/Extensions/FuseDockFSKit.appex"
pluginkit -e use -p com.apple.fskit.fsmodule -i "$extension_id" 2>/dev/null || true

printf 'Installed %s\n' "$dest_app"
printf 'If macOS still lists disabled extensions, quit and reopen System Settings.\n'
printf 'Enable FuseDock under By Category > File System Extensions.\n'
