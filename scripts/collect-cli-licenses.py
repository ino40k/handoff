#!/usr/bin/env python3
"""Collect pinned Go module licenses, preserving required public attribution."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
root = Path(__file__).resolve().parent.parent
target = Path(sys.argv[1])
target.mkdir(parents=True, exist_ok=True)
go = os.environ.get("GO", "go")
records = subprocess.check_output([go, "list", "-m", "-json", "all"], cwd=root / "backend").decode()
decoder = json.JSONDecoder()
while records.strip():
    record, end = decoder.raw_decode(records.lstrip())
    records = records.lstrip()[end:]
    if not record.get("Dir"):
        continue
    for source in Path(record["Dir"]).iterdir():
        if source.is_file() and source.name.lower().startswith(("license", "copying", "notice")):
            dest = target / record["Path"] / record.get("Version", "local") / source.name
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, dest)
goroot = subprocess.check_output([go, "env", "GOROOT"], text=True).strip()
shutil.copyfile(Path(goroot) / "LICENSE", target / "Go-LICENSE")

patents = Path(goroot) / "PATENTS"
if patents.is_file():
    shutil.copyfile(patents, target / "Go-PATENTS")
# A fixed, complete notice file is easy to install safely without extracting an
# arbitrary tar directory tree as root. Preserve each license's original bytes.
with (target.parent / "THIRD-PARTY-LICENSES.txt").open("wb") as output:
    for license_file in sorted(target.rglob("*")):
        if license_file.is_file():
            output.write(("\n===== " + license_file.relative_to(target).as_posix() + " =====\n").encode())
            output.write(license_file.read_bytes())
            output.write(b"\n")
