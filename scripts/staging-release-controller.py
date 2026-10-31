#!/usr/bin/env python3
"""Staging release controller, executed by forced-command as asteria-deploy.

The controller is root-owned so deploy cannot edit it. Its state file is a
 deploy-owned operational journal, not a root trust anchor. Every ACTIVE decision
is re-derived from the live runtime. The deploy user's docker membership already
confers host-administrator-equivalent power; the remote forced command is the
security boundary, not a fictional root/deploy privilege separation.
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

from staging_runtime_verifier import RuntimeVerificationError, RuntimeVerifier

FORMAT = "asteria-drive-staging-controller/v1"
ACTIVE = "ACTIVE"
ATTEMPTING = "ATTEMPTING"
RECOVERY_REQUIRED = "RECOVERY_REQUIRED"


def fail(message: str) -> None:
    print(f"staging controller rejected: {message}", file=sys.stderr)
    raise SystemExit(1)


def require_regular_file(path: Path, label: str, executable: bool = False) -> None:
    try:
        info = path.lstat()
    except FileNotFoundError:
        fail(f"{label} is missing")
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        fail(f"{label} is not a regular file")
    if executable and info.st_mode & 0o777 != 0o755:
        fail(f"{label} has unsafe executable mode")


def atomic_write(path: Path, value: dict, deploy_root: Path) -> None:
    if path.parent != deploy_root:
        fail("controller state must be a direct child of the deploy root")
    if deploy_root.is_symlink() or not deploy_root.is_dir():
        fail("deploy root is missing or symlinked")
    try:
        if deploy_root.resolve(strict=True) != deploy_root:
            fail("deploy root resolves through a symlink")
    except OSError:
        fail("deploy root cannot be resolved")
    if hasattr(os, "geteuid") and deploy_root.stat().st_uid != os.geteuid():
        fail("deploy root is not owned by the controller user")
    if os.name == "posix" and deploy_root.stat().st_mode & 0o022:
        fail("deploy root must not be group- or world-writable")
    data = json.dumps(value, indent=2, sort_keys=True).encode() + b"\n"
    descriptor, temporary = tempfile.mkstemp(prefix=".controller-state-", dir=deploy_root)
    try:
        os.chmod(temporary, 0o600)
        with os.fdopen(descriptor, "wb") as handle:
            descriptor = -1
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        try:
            directory = os.open(deploy_root, os.O_RDONLY)
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


def load_state(path: Path, deploy_root: Path) -> dict:
    if not path.exists():
        return {"format": FORMAT, "state": "IDLE"}
    require_regular_file(path, "controller state")
    info = path.stat()
    if hasattr(os, "geteuid") and info.st_uid != os.geteuid():
        fail("controller state is not owned by the controller user")
    if os.name == "posix" and stat.S_IMODE(info.st_mode) != 0o600:
        fail("controller state must have mode 0600")
    if path.parent != deploy_root:
        fail("controller state has an unsafe path")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        fail("controller state is unreadable or corrupt")
    if value.get("format") != FORMAT or value.get("state") not in {ACTIVE, ATTEMPTING, RECOVERY_REQUIRED, "IDLE"}:
        fail("controller state has an unsupported format")
    # A deploy user can alter this journal. Never use it as the sole proof of
    # ACTIVE or of a completed operation; runtime verification is mandatory.
    return value


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(65536), b""):
            digest.update(chunk)
    return digest.hexdigest()


def active_version(compose: Path, releases: dict) -> tuple[str, str]:
    require_regular_file(compose, "active compose")
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
    require_regular_file(evidence, "deployment evidence")
    raw = evidence.read_bytes()
    if len(raw) > 2 * 1024 * 1024:
        fail("deployment evidence is too large")
    try:
        value = json.loads(raw)
    except ValueError:
        fail("deployment evidence is not JSON")
    return hashlib.sha256(raw).hexdigest(), value


def recover(state_path: Path, deploy_root: Path, record: dict, reason: str) -> int:
    record.update({"state": RECOVERY_REQUIRED, "last_error": reason})
    atomic_write(state_path, record, deploy_root)
    return 1


def commit_active(state_path: Path, deploy_root: Path, record: dict, snapshot: dict, evidence_sha256: str) -> dict:
    # One complete atomic record, never ACTIVE in a first write and identity in a second.
    record.update({
        "state": ACTIVE,
        "active_version": snapshot["version"],
        "compose_sha256": snapshot["compose_sha256"],
        "image_ref": snapshot["image_ref"],
        "image_id": snapshot["image_id"],
        "schema_version": snapshot["schema_version"],
        "readiness": snapshot["readiness"],
        "evidence_sha256": evidence_sha256,
        "trust_boundary": "deploy-owned-journal-runtime-reverification-required",
    })
    atomic_write(state_path, record, deploy_root)
    return record


def verify_runtime(runtime: RuntimeVerifier, version: str, compose_sha256: str, releases: dict) -> dict:
    bundle = releases[version]
    snapshot = runtime.snapshot(version, bundle)
    snapshot["compose_sha256"] = compose_sha256
    if snapshot.get("version") != version:
        raise RuntimeVerificationError("runtime version does not match active Compose")
    if snapshot.get("image_ref") != bundle["image"] or snapshot.get("schema_version") != bundle["schema_version"]:
        raise RuntimeVerificationError("runtime identity does not match bundle")
    if snapshot.get("container_state") != "running":
        raise RuntimeVerificationError("API container is not running")
    if snapshot.get("container_health") not in ("healthy", "none"):
        raise RuntimeVerificationError("API container is not healthy")
    if snapshot.get("readiness") is not True:
        raise RuntimeVerificationError("API readiness is not proven")
    return snapshot


def common_evidence(report: dict, run_id: str, attempt: str, workflow_sha: str, expected: dict) -> str | None:
    if report.get("schema") != "asteria-drive-staging-deployment/v2":
        return "deployment evidence schema is wrong"
    if not isinstance(report.get("last_phase"), str) or not report.get("last_phase"):
        return "deployment evidence has no last phase"
    if report.get("github_run_id") != run_id or report.get("github_run_attempt") != attempt:
        return "deployment evidence run identity mismatch"
    if report.get("workflow_sha") != workflow_sha:
        return "deployment evidence workflow identity mismatch"
    if report.get("release_tag") != expected["version"] or report.get("source_commit") != expected["source_commit"]:
        return "deployment evidence release/source mismatch"
    return None


def success_evidence(report: dict, snapshot: dict, expected: dict) -> str | None:
    if report.get("status") != "success" or report.get("last_phase") != "complete":
        return "deployment did not prove success/complete"
    if report.get("image") != expected["image"]:
        return "deployment evidence candidate image does not match bundle"
    if report.get("compose_sha256") != snapshot["compose_sha256"]:
        return "deployment evidence compose does not match runtime"
    if report.get("deployed_image_ref") != snapshot["image_ref"] or report.get("deployed_image_id") != snapshot["image_id"]:
        return "deployment evidence image does not match runtime"
    if report.get("migration_schema_after") != snapshot["schema_version"]:
        return "deployment evidence migration schema does not match runtime"
    if report.get("health_succeeded") is not True or report.get("readiness_succeeded") is not True:
        return "deployment evidence does not prove health/readiness"
    return None


def main(runtime: RuntimeVerifier | None = None) -> int:
    if len(sys.argv) != 7:
        fail("usage: deploy <run> <attempt> <workflow_sha> <target_version> <run_dir>")
    _, operation, run_id, attempt, workflow_sha, target_version, run_dir_text = sys.argv
    if operation != "deploy" or not re.fullmatch(r"[0-9]{1,20}", run_id) or not re.fullmatch(r"[0-9]{1,6}", attempt):
        fail("invalid run identity")
    if not re.fullmatch(r"[0-9a-f]{40}", workflow_sha):
        fail("invalid workflow identity")
    releases_path = Path(os.environ.get("ASTERIA_CONTROLLER_RELEASES", "/etc/asteria-drive/staging-releases.json"))
    require_regular_file(releases_path, "release manifest")
    try:
        releases = json.loads(releases_path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        fail("release manifest is unreadable")
    if target_version not in releases:
        fail("target version is not approved")
    expected = dict(releases[target_version])
    expected["version"] = target_version
    deploy_root = Path(os.environ.get("ASTERIA_DEPLOY_ROOT", "/opt/asteria-drive/staging"))
    state_path = Path(os.environ.get("ASTERIA_CONTROLLER_STATE", str(deploy_root / "controller-state.json")))
    run_dir = Path(run_dir_text)
    compose = run_dir / "compose.yaml"
    deploy_script = run_dir / "deploy-staging.sh"
    evidence = run_dir / "deployment-evidence.json"
    require_regular_file(compose, "candidate compose")
    require_regular_file(deploy_script, "candidate deploy script")
    if sha256_file(compose) != expected["compose_sha256"]:
        fail("candidate compose does not match target bundle")
    if sha256_file(deploy_script) != expected["deploy_script_sha256"]:
        fail("candidate script does not match target bundle")
    if runtime is None:
        runtime = RuntimeVerifier(deploy_root)
    state = load_state(state_path, deploy_root)
    if state.get("state") == RECOVERY_REQUIRED:
        fail("manual recovery is required")
    if state.get("state") == ATTEMPTING:
        fail("an interrupted operation requires manual recovery")
    active_compose = deploy_root / "compose.yaml"
    try:
        current, current_compose_sha = active_version(active_compose, releases)
        current_snapshot = verify_runtime(runtime, current, current_compose_sha, releases)
    except (RuntimeVerificationError, OSError, ValueError) as error:
        fail(f"current runtime is not approved and healthy: {error}")
    previous = current_snapshot["version"]
    exact_replay = (
        state.get("run_id") == run_id and state.get("attempt") == attempt
        and state.get("workflow_sha") == workflow_sha and state.get("operation") == operation
        and state.get("target_version") == target_version
    )
    if state.get("state") == ACTIVE and exact_replay:
        try:
            snapshot = verify_runtime(runtime, previous, current_compose_sha, releases)
        except (RuntimeVerificationError, OSError, ValueError) as error:
            fail(f"replay runtime verification failed: {error}")
        if previous != target_version:
            fail("recorded active state does not match runtime")
        if state.get("active_version") != previous or state.get("compose_sha256") != current_compose_sha or state.get("image_ref") != snapshot["image_ref"]:
            fail("journal identity does not match runtime")
        return 0
    if previous == target_version:
        fail("target is already active without an exact replay")

    record = {
        "format": FORMAT, "operation": operation, "run_id": run_id, "attempt": attempt,
        "workflow_sha": workflow_sha, "target_version": target_version,
        "expected_current_version": previous, "previous_version": previous,
        "state": ATTEMPTING, "active_version": previous,
        "compose_sha256": current_compose_sha, "image_ref": current_snapshot["image_ref"],
        "schema_version": current_snapshot["schema_version"], "evidence_sha256": "",
    }
    atomic_write(state_path, record, deploy_root)
    environment = dict(os.environ)
    environment["ASTERIA_DEPLOY_ROOT"] = str(deploy_root)
    result = subprocess.run(
        ["bash", str(deploy_script), str(compose), str(evidence), run_id, attempt, workflow_sha],
        env=environment, capture_output=True, text=True,
    )
    if result.returncode != 0:
        if result.stderr:
            print(result.stderr[-4096:], file=sys.stderr)
        if evidence.is_file():
            digest, report = evidence_digest(evidence)
            identity_error = common_evidence(report, run_id, attempt, workflow_sha, expected)
            if report.get("status") != "failed":
                identity_error = identity_error or "rollback evidence status is not failed"
            if report.get("compose_sha256") != expected["compose_sha256"]:
                identity_error = identity_error or "rollback evidence candidate compose mismatch"
            if identity_error:
                return recover(state_path, deploy_root, record, identity_error)
            if report.get("rollback_succeeded") is True:
                try:
                    restored, restored_compose_sha = active_version(active_compose, releases)
                    snapshot = verify_runtime(runtime, restored, restored_compose_sha, releases)
                    if restored != previous:
                        raise RuntimeVerificationError("restored runtime is not the previous version")
                    if report.get("previous_image") != snapshot["image_ref"]:
                        raise RuntimeVerificationError("rollback evidence does not match restored image")
                    # deployed_image_ref is only populated after post-start identity
                    # verification, so a legitimate early rollback records "none".
                    if report.get("deployed_image_ref") not in ("none", snapshot["image_ref"]):
                        raise RuntimeVerificationError("rollback deployed image is inconsistent")
                    if report.get("deployed_image_id") not in ("none", snapshot["image_id"]):
                        raise RuntimeVerificationError("rollback deployed image ID is inconsistent")
                    if report.get("migration_schema_after") != snapshot["schema_version"]:
                        raise RuntimeVerificationError("rollback evidence schema does not match restored schema")
                except (RuntimeVerificationError, OSError, ValueError) as error:
                    return recover(state_path, deploy_root, record, f"rollback verification failed: {error}")
                record["evidence_sha256"] = digest
                record["last_error"] = "activation failed; previous runtime independently verified"
                commit_active(state_path, deploy_root, record, snapshot, digest)
                return 1
        return recover(state_path, deploy_root, record, "activation failed without a verified previous state")

    digest, report = evidence_digest(evidence)
    error = common_evidence(report, run_id, attempt, workflow_sha, expected)
    if error:
        return recover(state_path, deploy_root, record, error)
    try:
        current, current_compose_sha = active_version(active_compose, releases)
        snapshot = verify_runtime(runtime, current, current_compose_sha, releases)
        if current != target_version:
            raise RuntimeVerificationError("active compose does not match target")
        error = success_evidence(report, snapshot, expected)
        if error:
            return recover(state_path, deploy_root, record, error)
    except (RuntimeVerificationError, OSError, ValueError) as error:
        return recover(state_path, deploy_root, record, f"post-activation verification failed: {error}")
    commit_active(state_path, deploy_root, record, snapshot, digest)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
