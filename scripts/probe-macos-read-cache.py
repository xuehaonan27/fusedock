#!/usr/bin/env python3
"""Compare file bytes and callbacks using the read-content diagnostic app."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time


def observe(fd):
    try:
        size = os.fstat(fd).st_size
        return {"size": size, "prefix": os.pread(fd, 4, 0).decode(),
                "uncached": os.pread(fd, 4, 8 << 20).decode(),
                "eof": os.pread(fd, 1, size).decode(),
                "old_eof": os.pread(fd, 1, (8 << 20) + 17).decode()}
    except OSError as error:
        return {"errno": error.errno, "error": str(error)}


def open_view(path):
    try:
        fd = os.open(path, os.O_RDONLY)
    except OSError as error:
        return {"errno": error.errno, "error": str(error)}
    try:
        return observe(fd)
    finally:
        os.close(fd)


def run_case(output, log_path, action):
    task = Path(tempfile.mkdtemp(prefix=action + "-", dir=output))
    resource = task / task.name
    resource.mkdir(mode=0o700)
    (resource / "config").write_text("root-first\n")
    (resource / "state").write_text("1 none\n")
    point = task / "mount"
    point.mkdir()
    old = {}
    try:
        subprocess.run(["/sbin/mount", "-t", "fusedock", str(resource), str(point)], check=True, timeout=20)
        paths = {"root": point / "file.txt", "child": point / "child/file.txt"}
        initial = {}
        for name, path in paths.items():
            old[name] = os.open(path, os.O_RDONLY)
            initial[name] = {"prefix": os.pread(old[name], 4, 0).decode(), "size": os.fstat(old[name]).st_size}
            assert initial[name] == {"prefix": "AAAA", "size": (8 << 20) + 17}, initial
        staged = resource / "state.new"
        staged.write_text(f"2 {action}\n")
        staged.replace(resource / "state")
        marker = f"probe={resource.name} event=published revision=2 action={action}"
        deadline = time.monotonic() + 8
        while marker not in log_path.read_text():
            if time.monotonic() > deadline:
                raise RuntimeError("The probe did not publish revision 2")
            time.sleep(0.05)
        # All cache actions have completed before these opens.
        new = {name: open_view(path) for name, path in paths.items()}
        retained = {name: observe(fd) for name, fd in old.items()}
        fresh = {name: open_view(path.with_name("fresh.txt")) for name, path in paths.items()}
        retained_after_fresh = {name: observe(fd) for name, fd in old.items()}
        for fd in old.values():
            os.close(fd)
        old.clear()
        after_close = {name: open_view(path) for name, path in paths.items()}
        deadline = time.monotonic() + 2
        while any(view.get("prefix") != "BBBB" for view in after_close.values()) and time.monotonic() < deadline:
            time.sleep(0.05)
            after_close = {name: open_view(path) for name, path in paths.items()}
        result = {"action": action, "probe_id": resource.name, "initial": initial,
                  "reopened": new, "retained": retained, "fresh_name": fresh,
                  "retained_after_fresh": retained_after_fresh, "after_close": after_close}
        print(json.dumps(result, indent=2), flush=True)
        return result
    finally:
        for fd in old.values():
            os.close(fd)
        if os.path.ismount(point):
            subprocess.run(["/sbin/umount", str(point)], check=True, timeout=15)
        shutil.rmtree(task)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True, help="New directory for logs and JSON")
    parser.add_argument("--action", action="append", choices=("none", "directory", "file", "revoke"))
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True)
    results = []
    log_path = output / "probe.log"
    with log_path.open("w") as log_file:
        stream = subprocess.Popen(["/usr/bin/log", "stream", "--style", "compact", "--level", "debug",
                                   "--predicate", 'subsystem == "dev.fusedock.root-cache-probe"'],
                                  stdout=log_file, stderr=subprocess.STDOUT)
        try:
            time.sleep(0.5)
            for action in args.action or ("none", "directory", "file", "revoke"):
                results.append(run_case(output, log_path, action))
        finally:
            stream.terminate()
            stream.wait(timeout=10)
            (output / "result.json").write_text(json.dumps(results, indent=2) + "\n")
    # Compare complete byte/size/EOF observations, including the retained readers.
    expected_old = {"size": (8 << 20) + 17, "prefix": "AAAA", "uncached": "AAAA", "eof": "", "old_eof": ""}
    expected_new = {"size": (8 << 20) + 32, "prefix": "BBBB", "uncached": "BBBB", "eof": "", "old_eof": "B"}
    for result in results:
        result["snapshot_contract"] = all(
            result["reopened"][parent] == expected_new and result["retained"][parent] == expected_old
            and result["retained_after_fresh"][parent] == expected_old
            for parent in ("root", "child"))
    (output / "result.json").write_text(json.dumps(results, indent=2) + "\n")
    return 0 if all(result["snapshot_contract"] for result in results) else 1


if __name__ == "__main__":
    raise SystemExit(main())
