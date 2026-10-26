#!/usr/bin/env python3
"""Rootless Podman/systemd installation tests using synthetic, disposable state.
Requires podman, Python 3 and a built CLI distribution. Never installs on host.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import pty
import re
import select
import subprocess
import tarfile
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parent.parent
VERSION = re.search(r'const Version = "([^"]+)"', (ROOT / "backend/internal/headless/daemon_linux.go").read_text()).group(1)
ASSET = f"handoff-cli-{VERSION}-linux-amd64.tar.gz"
PARSER = argparse.ArgumentParser(description=__doc__)
PARSER.add_argument("--existing", nargs="*", help="Use only disposable, dedicated test containers already running with /src mounted")
ARGS = PARSER.parse_args()


def pod(*args, input=None, ok=True):
    result = subprocess.run(["podman", *args], input=input, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if ok and result.returncode:
        # Avoid dumping private test state or secret invitations in error output.
        raise RuntimeError(f"Podman operation failed ({result.returncode}): {args[0]}")
    return result


def command(host, *args, input=None, ok=True):
    return pod("exec", "-i", host, *args, input=input, ok=ok)


def cli(host, *args, json_out=False, ok=True, input=None):
    result = command(host, "handoff-cli", *(('--json',) if json_out else ()), *args, input=input, ok=ok)
    return json.loads(result.stdout) if json_out else result


def wait(fn, label, seconds=150):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if fn():
                return
        except (RuntimeError, json.JSONDecodeError):
            pass
        time.sleep(.2)
    raise RuntimeError(f"Timed out: {label}")


def install(host, first=False, archive=None, checksums=None, ok=True):
    args = ["podman", "exec"]
    if first:
        args.append("-it")
    args += [host, "sh", "/src/dist/install-cli.sh", "--archive", archive or f"/src/dist/{ASSET}", "--checksums", checksums or "/src/dist/SHA256SUMS-cli.txt"]
    if not first:
        result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    else:
        master, slave = pty.openpty()
        proc = subprocess.Popen(args, stdin=slave, stdout=slave, stderr=slave)
        os.close(slave)
        # Reads go through /dev/tty, even when the online installer uses a pipe.
        transcript = bytearray()
        sent = False
        deadline = time.monotonic() + 60
        while proc.poll() is None and time.monotonic() < deadline:
            if select.select([master], [], [], .2)[0]:
                try:
                    transcript.extend(os.read(master, 65536))
                except OSError:
                    break
            if not sent and b"Service account [" in transcript:
                os.write(master, b"\nExample server\n\ny\n")
                sent = True
        if proc.poll() is None:
            proc.kill()
        proc.wait()
        os.close(master)
        result = subprocess.CompletedProcess(args, proc.returncode)
    if ok and result.returncode:
        raise RuntimeError(f"Installer failed in disposable container {host}")
    return result.returncode


def state(host):
    # Secrets stay in test process memory, never printed or written into source.
    return json.loads(command(host, "cat", "/var/lib/handoff-cli/state.json").stdout)


def visible(host):
    transfers = cli(host, "status", json_out=True)["status"]["Transfers"]
    keys = ("ID", "Status", "Approved", "Paused", "Direction", "Done", "Size")
    return sorted(({k: t[k] for k in keys} for t in transfers), key=lambda t: t["ID"])


def stable(st):
    return {"key": st["identity"]["key"], "psk": st["identity"]["psk"], "settings": st["settings"], "peers": st["peers"], "transfers": st["transfers"]}


def verify_service(host):
    refused = command(host, "handoff-cli", "serve", "--data-dir", "/tmp/unused-root-profile", ok=False)
    assert refused.returncode != 0 and b"root" in refused.stderr, "root daemon was not refused"
    command(host, "systemctl", "is-active", "--quiet", "handoff-cli")
    command(host, "systemctl", "is-enabled", "--quiet", "handoff-cli")
    pid = command(host, "systemctl", "show", "handoff-cli", "-p", "MainPID", "--value").stdout.decode().strip()
    status = command(host, "cat", f"/proc/{pid}/status").stdout.decode()
    fields = dict(line.split(":", 1) for line in status.splitlines() if ":" in line)
    assert int(fields["Uid"].split()[0]) != 0
    assert int(fields["CapEff"].strip(), 16) == int(fields["CapBnd"].strip(), 16) == 0
    assert fields["NoNewPrivs"].strip() == "1"
    command(host, "runuser", "-u", "handoff-cli", "--", "handoff-cli", "status")
    command(host, "useradd", "--system", "outsider", ok=False)
    result = command(host, "runuser", "-u", "outsider", "--", "handoff-cli", "status", ok=False)
    assert result.returncode != 0, "untrusted account reached control socket"


created = []
hosts = ARGS.existing or []
try:
    if not hosts:
        for distro, base in (("ubuntu", "docker.io/library/ubuntu:24.04"), ("debian", "docker.io/library/debian:13")):
            image = f"localhost/handoff-cli-install-test-{distro}"
            pod("build", "--build-arg", f"BASE={base}", "-f", str(ROOT / "packaging/cli/Containerfile.test"), "-t", image, str(ROOT / "packaging/cli"))
            name = f"handoff-cli-test-{distro}-{uuid.uuid4().hex[:8]}"
            pod("run", "-d", "--name", name, "--systemd=always", "--security-opt", "label=disable", "-v", f"{ROOT}:/src:ro", image)
            hosts.append(name)
            created.append(name)
    for host in hosts:
        first = command(host, "test", "-f", "/etc/handoff-cli/service-user", ok=False).returncode != 0
        install(host, first=first)
        verify_service(host)
        command(host,"test","-s","/usr/local/share/doc/handoff-cli/THIRD-PARTY-LICENSES.txt")
        command(host, "shellcheck", "/src/scripts/install-cli.sh", "/src/scripts/uninstall-cli.sh", "/src/scripts/build-cli.sh")
        cli(host, "settings", "--name", "Server example")
        # Damaged download must fail before changing the installed executable.
        digest = command(host, "sha256sum", "/usr/local/bin/handoff-cli").stdout
        command(host, "sh", "-c", f"cp /src/dist/{ASSET} /tmp/bad.tar.gz && printf corrupt >> /tmp/bad.tar.gz")
        assert install(host, archive="/tmp/bad.tar.gz", ok=False) != 0
        assert command(host, "sha256sum", "/usr/local/bin/handoff-cli").stdout == digest
        print("PASS: install, non-root boot unit, zero capabilities, control permissions, checksum rejection, lint", flush=True)
    # Use the two OS services as real tailcat peers, not as a shared tailnet.
    if len(hosts) >= 2:
        sender, receiver = hosts[:2]
        invitation = cli(sender, "invite").stdout
        cli(receiver, "join", input=invitation)
        def awaiting():
            return any(p["status"] == "approval" for p in cli(sender, "pairings", json_out=True))
        wait(awaiting, "pair request")
        pair = next(p for p in cli(sender, "pairings", json_out=True) if p["status"] == "approval")
        cli(sender, "approve", pair["id"])
        wait(lambda: len(cli(sender, "peers", json_out=True)) == 1 and len(cli(receiver, "peers", json_out=True)) == 1, "paired")
        peer = cli(sender, "peers", json_out=True)[0]["ID"]
        command(sender, "runuser", "-u", "handoff-cli", "--", "sh", "-c", "mkdir -p /var/lib/handoff-cli/fixture-source/empty; dd if=/dev/zero of='/var/lib/handoff-cli/fixture-source/file ü.bin' bs=1048576 count=12 status=none")
        cli(sender, "send", peer, "/var/lib/handoff-cli/fixture-source")
        wait(lambda: bool(cli(receiver, "pending", json_out=True)), "pending offer")
        incoming = cli(receiver, "pending", json_out=True)[0]["ID"]
        # Pending approval and paired identity survive an actual installer upgrade.
        before = stable(state(receiver))
        before_visible = visible(receiver)
        install(receiver)
        after = stable(state(receiver))
        assert before == after, "upgrade changed pending request, peers, identity or settings"
        assert visible(receiver) == before_visible, "upgrade changed visible pending state"
        cli(receiver, "accept", incoming)
        cli(receiver, "pause", incoming)
        before = stable(state(receiver))
        before_visible = visible(receiver)
        install(receiver)
        assert stable(state(receiver)) == before, "upgrade changed accepted/paused state"
        assert visible(receiver) == before_visible, "upgrade changed journaled approval/pause/progress"
        cli(receiver, "resume", incoming)
        wait(lambda: any(t["ID"] == incoming and t["Status"] == "completed" for t in cli(receiver, "history", json_out=True)), "resumed transfer")
        a = command(sender, "sha256sum", "/var/lib/handoff-cli/fixture-source/file ü.bin").stdout.split()[0]
        b = command(receiver, "sha256sum", "/var/lib/handoff-cli/received/fixture-source/file ü.bin").stdout.split()[0]
        assert a == b
        wait(lambda: any(t["ID"] == incoming and t["Status"] == "completed" for t in cli(sender, "history", json_out=True)), "sender completion")
        print("PASS: Ubuntu/Debian service pairing, approval, directory transfer, pending/accepted upgrades and integrity", flush=True)
    for host in hosts:
        before = stable(state(host))
        install(host)
        assert stable(state(host)) == before
        # A checksum-valid but broken new binary must roll back both executable
        # and unit, without touching state. This package exists only in /tmp.
        with tempfile.TemporaryDirectory(prefix="handoff-cli-rollback-") as tmp:
            bad = Path(tmp) / "bad.tar.gz"
            with tarfile.open(ROOT / "dist" / ASSET) as source, tarfile.open(bad, "w:gz") as target:
                for item in source:
                    content = source.extractfile(item).read() if item.isfile() else None
                    if item.name == "handoff-cli/handoff-cli":
                        content = b'#!/bin/sh\nif [ "$1" = version ]; then echo "synthetic installer test"; exit 0; fi\nexit 2\n'
                        item.size = len(content)
                    target.addfile(item, io.BytesIO(content) if content is not None else None)
            checksum = Path(tmp) / "checksums.txt"
            checksum.write_text(hashlib.file_digest(bad.open("rb"), "sha256").hexdigest() + "  " + ASSET + "\n")
            pod("cp", str(bad), host + ":/tmp/failing-archive.tar.gz")
            pod("cp", str(checksum), host + ":/tmp/failing-checksums.txt")
            digest = command(host, "sha256sum", "/usr/local/bin/handoff-cli").stdout
            assert install(host, archive="/tmp/failing-archive.tar.gz", checksums="/tmp/failing-checksums.txt", ok=False) != 0
            verify_service(host)
            assert command(host, "sha256sum", "/usr/local/bin/handoff-cli").stdout == digest
            assert stable(state(host)) == before
        # A container restart exercises real boot enablement without a login.
        pod("restart", host)
        wait(lambda: command(host, "systemctl", "is-active", "--quiet", "handoff-cli", ok=False).returncode == 0, "boot startup")
        verify_service(host)
        assert stable(state(host))["key"] == before["key"]
        command(host, "sh", "/usr/local/lib/handoff-cli/uninstall-cli.sh")
        assert stable(state(host)) == before
        assert command(host, "test", "-e", "/usr/local/bin/handoff-cli", ok=False).returncode != 0
        install(host)
        verify_service(host)
        assert stable(state(host)) == before
        print("PASS: upgrade, boot without login, uninstall/reinstall preserving identity, configuration and history", flush=True)
finally:
    for name in created:
        pod("rm", "-f", name, ok=False)
