#!/usr/bin/env python3
"""Exercise the signed standalone RootCacheProbe app; see docs/fskit-root-cache.md."""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time


CASES = ("root-first", "child-first", "root-no-cache", "root-id-42", "root-parent-self")
ACTIONS = {0: "push", 1: "pushInvalidate", 2: "invalidate", 3: "update"}
LOG_PREDICATE = 'subsystem == "dev.fusedock.root-cache-probe"'
SAMPLE = re.compile(
    r"probe=(\S+) rootID=(\d+) parentID=(\d+) rootFirst=(\S+) "
    r"rootNoCache=(\S+) item=(root|child) action=(\d+) domain=(\S+) code=(-?\d+)"
)


def samples(log_path, probe_id):
    found = {}
    for match in SAMPLE.finditer(log_path.read_text()):
        probe, root_id, parent_id, root_first, root_no_cache, item, action, domain, code = match.groups()
        if probe == probe_id:
            found[(item, int(action))] = {
                "item": item, "action": ACTIONS[int(action)], "domain": domain, "code": int(code),
                "root_id": int(root_id), "parent_id": int(parent_id),
                "root_first": root_first, "root_no_cache": root_no_cache,
            }
    return found


def run_case(output, log_path, case, resource_kind):
    task = Path(tempfile.mkdtemp(prefix=f"{case}-{resource_kind}-", dir=output))
    resource = task / task.name  # Unique log correlation ID, for either resource kind.
    config = resource
    if resource_kind == "directory":
        resource.mkdir(mode=0o700)
        config = resource / "config"
    config.write_text(case + "\n")
    config.chmod(0o600)
    point = task / "mount"
    point.mkdir()
    try:
        subprocess.run(["/sbin/mount", "-t", "fusedock", str(resource), str(point)], check=True, timeout=20)
        entries = {"root": os.listdir(point), "child": os.listdir(point / "child")}
        deadline = time.monotonic() + 6
        observed = {}
        while time.monotonic() < deadline:
            observed = samples(log_path, resource.name)
            if len(observed) == 8 and all(observed[("child", action)]["code"] == 0 for action in ACTIONS):
                break
            time.sleep(0.1)
        passed = len(observed) == 8 and all(sample["code"] == 0 for sample in observed.values())
        result = {
            "case": case, "resource_kind": resource_kind, "probe_id": resource.name,
            "entries": entries, "passed": passed,
            "samples": [observed[key] for key in sorted(observed)],
        }
        codes = {item: sorted({sample["code"] for sample in observed.values() if sample["item"] == item})
                 for item in ("root", "child")}
        print(f"{case}/{resource_kind}: root={codes['root']} child={codes['child']} "
              f"samples={len(observed)}/8 {'PASS' if passed else 'FAIL'}", flush=True)
        return result
    finally:
        if os.path.ismount(point):
            subprocess.run(["/sbin/umount", str(point)], check=True, timeout=15)
        # An unsuccessful unmount raises above, leaving the mount and fixture intact.
        shutil.rmtree(task)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, help="New directory for logs and result.json")
    parser.add_argument("--case", action="append", choices=CASES, dest="cases")
    parser.add_argument("--resource-kind", choices=("file", "directory", "both"), default="both")
    args = parser.parse_args()
    if args.output is None:
        output = Path(tempfile.mkdtemp(prefix="fusedock-root-probe-"))
    else:
        output = args.output.resolve()
        output.mkdir(parents=True)
    print(f"Evidence: {output}", flush=True)
    report = {
        "os": subprocess.check_output(["/usr/bin/sw_vers"], text=True).strip(),
        "architecture": os.uname().machine, "cases": [],
    }
    log_path = output / "probe.log"
    with log_path.open("w") as log_file:
        stream = subprocess.Popen(
            ["/usr/bin/log", "stream", "--style", "compact", "--level", "debug", "--predicate", LOG_PREDICATE],
            stdout=log_file, stderr=subprocess.STDOUT,
        )
        try:
            time.sleep(0.5)  # Subscribe before mounting the probe.
            kinds = ("file", "directory") if args.resource_kind == "both" else (args.resource_kind,)
            for case in args.cases or CASES:
                for resource_kind in kinds:
                    report["cases"].append(run_case(output, log_path, case, resource_kind))
        finally:
            stream.terminate()
            stream.wait(timeout=10)
            (output / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    return 0 if all(case["passed"] for case in report["cases"]) else 1


if __name__ == "__main__":
    raise SystemExit(main())
