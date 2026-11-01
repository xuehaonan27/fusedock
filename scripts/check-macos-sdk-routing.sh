#!/bin/bash
set -euo pipefail

repo_dir=$(cd "$(dirname "$0")/.." && pwd)
xcodebuild -project "$repo_dir/macos/FSKit/FuseDockFSKit.xcodeproj" \
  -alltargets -configuration Debug -showBuildSettings -json |
python3 -c '
import json, sys
targets = json.load(sys.stdin)
assert {t["target"] for t in targets} == {"FuseDock", "FuseDockFSKit"}
for target in targets:
    settings = target["buildSettings"]
    sdk = int(settings["SDK_VERSION"].split(".")[0])
    flags = settings.get("SWIFT_ACTIVE_COMPILATION_CONDITIONS", "").split()
    assert sdk >= 26, (target["target"], sdk)
    assert ("FUSEDOCK_FSKIT27" in flags) == (sdk >= 27), (target["target"], sdk, flags)
    assert settings["MACOSX_DEPLOYMENT_TARGET"] == "26.0"
    print(target["target"], "SDK", sdk, "cache API", sdk >= 27, "minimum macOS 26")
'
