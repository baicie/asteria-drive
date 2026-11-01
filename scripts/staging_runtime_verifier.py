"""Host-side runtime verification for the staging release controller.

Docker is an administrative boundary: the deploy user is already docker-equivalent
on this host. This adapter therefore reports observations, not an independent root
attestation. The workflow still gates every change with digest-pinned executors.
"""
from __future__ import annotations

import json
import subprocess
from pathlib import Path

PROJECT = "asteria-drive-staging"
COMPOSE_PROJECT_OPTION = ["-p", PROJECT]


class RuntimeVerificationError(RuntimeError):
    pass


class RuntimeVerifier:
    def __init__(self, deploy_root: Path, runner=None, curl=None):
        self.deploy_root = Path(deploy_root)
        self.compose = self.deploy_root / "compose.yaml"
        self._runner = runner or self._run
        self._curl = curl or self._http

    def _run(self, args: list[str]) -> tuple[int, str]:
        try:
            result = subprocess.run(args, capture_output=True, text=True, timeout=30)
        except (OSError, subprocess.SubprocessError) as error:
            raise RuntimeVerificationError(f"runtime probe failed: {error}") from error
        return result.returncode, result.stdout.strip()

    def _http(self, path: str) -> bool:
        try:
            result = subprocess.run(
                ["curl", "--fail", "--silent", "--max-time", "3", f"http://127.0.0.1:18080{path}"],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10,
            )
        except (OSError, subprocess.SubprocessError) as error:
            raise RuntimeVerificationError(f"readiness probe failed: {error}") from error
        return result.returncode == 0

    def _inspect(self, container: str, expression: str) -> str:
        code, output = self._runner(["docker", "inspect", "--format", expression, container])
        if code != 0 or not output:
            raise RuntimeVerificationError(f"cannot inspect {container}")
        return output

    def snapshot(self, expected_version: str, bundle: dict) -> dict:
        api_id = self._runner(["docker", "compose", *COMPOSE_PROJECT_OPTION, "-f", str(self.compose), "ps", "-q", "api"])
        if api_id[0] != 0 or not api_id[1]:
            raise RuntimeVerificationError("API container is absent or stopped")
        container = api_id[1].splitlines()[0]
        expected_ref = bundle["image"]
        actual_ref = self._inspect(container, "{{.Config.Image}}")
        actual_id = self._inspect(container, "{{.Image}}")
        code, expected_id = self._runner(["docker", "image", "inspect", "--format", "{{.Id}}", expected_ref])
        if code != 0 or actual_ref != expected_ref or actual_id != expected_id:
            raise RuntimeVerificationError("running image identity does not match bundle")
        state = self._inspect(container, "{{.State.Status}}")
        health = self._inspect(container, "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}")
        if state != "running" or health not in ("healthy", "none"):
            raise RuntimeVerificationError("API container is not running and healthy")
        for service_port, expected in (("8080", "127.0.0.1:18080"), ("9090", "127.0.0.1:19090")):
            code, published = self._runner([
                "docker", "compose", *COMPOSE_PROJECT_OPTION, "-f", str(self.compose),
                "port", "api", service_port,
            ])
            if code != 0 or published != expected:
                raise RuntimeVerificationError(f"API port {service_port} is not loopback-bound")
        if not self._curl("/readyz") or not self._curl("/healthz"):
            raise RuntimeVerificationError("API readiness/health probe failed")
        code, schema = self._runner([
            "docker", "compose", *COMPOSE_PROJECT_OPTION, "-f", str(self.compose), "exec", "-T", "postgres",
            "psql", "-U", "asteria", "-d", "asteria", "-Atqc",
            "SELECT COALESCE(MAX(version),0) FROM asteria_schema_migration",
        ])
        if code != 0 or schema != str(bundle["schema_version"]):
            raise RuntimeVerificationError("database schema does not match bundle")
        return {
            "version": expected_version,
            "image_ref": actual_ref,
            "image_id": actual_id,
            "container_state": state,
            "container_health": health,
            "readiness": True,
            "schema_version": int(schema),
        }
