#!/usr/bin/env python3
"""Root-owned staging release controller.

This is the host-side admission and state machine for deploy/rollback. It wraps
the reviewed, version-specific deploy script; it never accepts arbitrary
commands, versions, images or digests.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

FORMAT = "asteria-drive-staging-controller/v1"
ACTIVE = "ACTIVE"
ATTEMPTING = "ATTEMPTING"
RECOVERY_REQUIRED = "RECOVERY_REQUIRED"


def fail(message: str) -> "NoReturn":
    print(f"staging controller rejected: {message}", file=sys.stderr)
    raise SystemExit(1)


def atomic_write(path: Path, value: dict) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    data = json.dumps(value, indent=2, sort_keys=True).encode() + b"\n"
    descriptor, temporary = tempfile.mkstemp(prefix=".controller-state-", dir=path.parent)
    try:
        os.chmod(temporary, 0o600)
        with os.fdopen(descriptor, "wb") as handle:
            descriptor = -1
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        try:
            directory = os.open(path.parent, os.O_RDONLY)
        except OSError:
            directory = -1
        if directory >= 0:
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
    finally:
        if descriptor >= 0:
            os.close(descriptor)
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def load_state(path: Path) -> dict:
    if not path.exists():
        return {"format": FORMAT, "state": "IDLE"}
    if path.is_symlink() or not path.is_file():
        fail("state path is not a regular file")
    if path.stat().st_mode & 0o077:
        fail("state file permissions are unsafe")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        fail("state file is unreadable or corrupt")
    if value.get("format") != FORMAT or value.get("state") not in {ACTIVE, ATTEMPTING, RECOVERY_REQUIRED, "IDLE"}:
        fail("state file has an unsupported format")
    return value


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(65536), b""):
            digest.update(chunk)
    return digest.hexdigest()


def active_version(compose: Path, releases: dict) -> tuple[str, str]:
    if compose.is_symlink() or not compose.is_file():
        fail("active compose is missing or symlinked")
    digest = sha256_file(compose)
    text = compose.read_text(encoding="utf-8")
    match = re.search(r"com\.baicie\.asteria\.release:\s*(v[0-9.]+)", text)
    if not match:
        fail("active compose has no release label")
    version = match.group(1)
    expected = releases.get(version)
    if not expected or expected.get("compose_sha256") != digest:
        fail("active compose is not an approved pair")
    return version, digest


def evidence_digest(evidence: Path) -> tuple[str, dict]:
    if evidence.is_symlink() or not evidence.is_file():
        fail("deployment evidence is missing")
    raw = evidence.read_bytes()
    if len(raw) > 2 * 1024 * 1024:
        fail("deployment evidence is too large")
    try:
        value = json.loads(raw)
    except ValueError:
        fail("deployment evidence is not JSON")
    return hashlib.sha256(raw).hexdigest(), value


def main() -> int:
    if len(sys.argv) != 7:
        fail("usage: deploy <run> <attempt> <workflow_sha> <target_version> <run_dir>")
    _, operation, run_id, attempt, workflow_sha, target_version, run_dir_text = sys.argv
    if operation != "deploy" or not re.fullmatch(r"[0-9]{1,20}", run_id) or not re.fullmatch(r"[0-9]{1,6}", attempt):
        fail("invalid run identity")
    if not re.fullmatch(r"[0-9a-f]{40}", workflow_sha):
        fail("invalid workflow identity")
    releases_path = Path(os.environ.get("ASTERIA_CONTROLLER_RELEASES", "/etc/asteria-drive/staging-releases.json"))
    if releases_path.is_symlink() or not releases_path.is_file():
        fail("release manifest is missing")
    try:
        releases = json.loads(releases_path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        fail("release manifest is unreadable")
    if target_version not in releases:
        fail("target version is not approved")
    run_dir = Path(run_dir_text)
    compose = run_dir / "compose.yaml"
    deploy_script = run_dir / "deploy-staging.sh"
    evidence = run_dir / "deployment-evidence.json"
    if compose.is_symlink() or deploy_script.is_symlink() or not compose.is_file() or not deploy_script.is_file():
        fail("candidate files are missing or symlinked")
    if sha256_file(compose) != releases[target_version]["compose_sha256"]:
        fail("candidate compose does not match target bundle")
    if sha256_file(deploy_script) != releases[target_version]["deploy_script_sha256"]:
        fail("candidate script does not match target bundle")

    state_path = Path(os.environ.get("ASTERIA_CONTROLLER_STATE", str(Path(os.environ.get("ASTERIA_DEPLOY_ROOT", "/opt/asteria-drive/staging")) / "controller-state.json")))
    state = load_state(state_path)
    if state.get("run_id") == run_id and state.get("attempt") == attempt and state.get("target_version") == target_version and state.get("state") == ACTIVE:
        current, _ = active_version(Path(os.environ.get("ASTERIA_DEPLOY_ROOT", "/opt/asteria-drive/staging")) / "compose.yaml", releases)
        if current != target_version:
            fail("recorded active state does not match host")
        return 0
    if state.get("state") == RECOVERY_REQUIRED:
        fail("manual recovery is required")
    if state.get("state") == ATTEMPTING:
        fail("an interrupted operation requires manual recovery")
    active_compose = Path(os.environ.get("ASTERIA_DEPLOY_ROOT", "/opt/asteria-drive/staging")) / "compose.yaml"
    previous = ""
    if active_compose.exists():
        current, _ = active_version(active_compose, releases)
        if current == target_version:
            fail("target is already active without a recorded ACTIVE state")
        previous = current
    if state.get("state") == ACTIVE and state.get("target_version") != target_version:
        previous = state.get("target_version")
        if previous not in releases:
            fail("recorded active version is not approved")

    record = {
        "format": FORMAT,
        "operation": operation,
        "run_id": run_id,
        "attempt": attempt,
        "workflow_sha": workflow_sha,
        "target_version": target_version,
        "previous_version": previous,
        "state": ATTEMPTING,
        "active_version": state.get("target_version", state.get("active_version", "")),
        "evidence_sha256": "",
    }
    atomic_write(state_path, record)
    environment = dict(os.environ)
    environment["ASTERIA_DEPLOY_ROOT"] = os.environ.get("ASTERIA_DEPLOY_ROOT", "/opt/asteria-drive/staging")
    result = subprocess.run(
        ["bash", str(deploy_script), str(compose), str(evidence), run_id, attempt, workflow_sha],
        env=environment,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        if result.stderr:
            print(result.stderr[-4096:], file=sys.stderr)
        if evidence.is_file():
            _, report = evidence_digest(evidence)
            if report.get("rollback_succeeded") is True:
                try:
                    current, digest = active_version(Path(environment["ASTERIA_DEPLOY_ROOT"]) / "compose.yaml", releases)
                except SystemExit:
                    current = ""
                    digest = ""
                if current in releases and current != target_version:
                    record.update({"state": ACTIVE, "active_version": current, "compose_sha256": digest, "evidence_sha256": sha256_file(evidence)})
                    atomic_write(state_path, record)
                    return 1
        record.update({"state": RECOVERY_REQUIRED, "last_error": "activation failed without a verified previous state"})
        atomic_write(state_path, record)
        return 1
    digest, report = evidence_digest(evidence)
    if report.get("status") != "success" or report.get("last_phase") != "complete":
        record.update({"state": RECOVERY_REQUIRED, "last_error": "deployment evidence did not prove success", "evidence_sha256": digest})
        atomic_write(state_path, record)
        return 1
    expected = releases[target_version]
    if report.get("release_tag") != target_version:
        record.update({"state": RECOVERY_REQUIRED, "last_error": "deployment evidence target mismatch", "evidence_sha256": digest})
        atomic_write(state_path, record)
        return 1
    if report.get("deployed_image_ref") != expected.get("image"):
        record.update({"state": RECOVERY_REQUIRED, "last_error": "deployment evidence image mismatch", "evidence_sha256": digest})
        atomic_write(state_path, record)
        return 1
    if report.get("compose_sha256") != expected.get("compose_sha256"):
        record.update({"state": RECOVERY_REQUIRED, "last_error": "deployment evidence compose mismatch", "evidence_sha256": digest})
        atomic_write(state_path, record)
        return 1
    if int(report.get("migration_schema_after", -1)) != int(expected.get("schema_version", -1)):
        record.update({"state": RECOVERY_REQUIRED, "last_error": "deployment evidence schema mismatch", "evidence_sha256": digest})
        atomic_write(state_path, record)
        return 1
    current, digest_active = active_version(Path(environment["ASTERIA_DEPLOY_ROOT"]) / "compose.yaml", releases)
    if current != target_version:
        record.update({"state": RECOVERY_REQUIRED, "last_error": "active compose does not match target", "evidence_sha256": digest})
        atomic_write(state_path, record)
        return 1
    record.update({"state": ACTIVE, "active_version": target_version, "compose_sha256": digest_active, "evidence_sha256": digest})
    atomic_write(state_path, record)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
