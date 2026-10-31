import hashlib
import importlib.util
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from staging_runtime_verifier import RuntimeVerificationError, RuntimeVerifier

spec = importlib.util.spec_from_file_location("staging_controller_under_test", ROOT / "scripts" / "staging-release-controller.py")
controller = importlib.util.module_from_spec(spec)
spec.loader.exec_module(controller)

V011 = "v0.1.1"
V020 = "v0.2.0"
IMAGE_011 = "ghcr.io/baicie/asteria-drive@sha256:" + "1" * 64
IMAGE_020 = "ghcr.io/baicie/asteria-drive@sha256:" + "2" * 64
SOURCE_011 = "a" * 40
SOURCE_020 = "b" * 40
WORKFLOW = "c" * 40
_MISSING = object()


def compose_text(version):
    return "name: asteria\nservices:\n  api:\n    labels:\n      com.baicie.asteria.release: " + version + "\n"


def file_hash(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


class FakeRuntime:
    def __init__(self, root):
        self.root = Path(root)
        self.calls = []
        self.image_drift = False
        self.schema = 3
        self.ready = True
        self.stopped = False
        self.container_state = "running"
        self.container_health = "healthy"

    def snapshot(self, version, bundle):
        self.calls.append(version)
        if self.stopped:
            raise RuntimeVerificationError("API container is absent or stopped")
        if self.image_drift:
            image = IMAGE_011 if version == V020 else IMAGE_020
        else:
            image = bundle["image"]
        return {
            "version": version,
            "image_ref": image,
            "image_id": "sha256:" + ("a" * 64 if version == V011 else "b" * 64),
            "container_state": self.container_state,
            "container_health": self.container_health,
            "readiness": self.ready,
            "schema_version": self.schema,
        }


class ControllerCase(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.root.chmod(0o700)
        self.run = self.root / "run"
        self.run.mkdir()
        self.active = self.root / "compose.yaml"
        self.active.write_text(compose_text(V011), encoding="utf-8")
        self.compose = self.run / "compose.yaml"
        self.compose.write_text(compose_text(V020), encoding="utf-8")
        self.executor = self.run / "deploy-staging.sh"
        self.executor.write_text("#!/usr/bin/env bash\n", encoding="utf-8")
        self.manifest = self.root / "releases.json"
        self.releases = {
            V011: {"compose_sha256": file_hash(self.active), "deploy_script_sha256": file_hash(self.executor), "image": IMAGE_011, "schema_version": 3, "source_commit": SOURCE_011},
            V020: {"compose_sha256": file_hash(self.compose), "deploy_script_sha256": file_hash(self.executor), "image": IMAGE_020, "schema_version": 3, "source_commit": SOURCE_020},
        }
        self.manifest.write_text(json.dumps(self.releases), encoding="utf-8")
        self.state = self.root / "controller-state.json"
        self.runtime = FakeRuntime(self.root)
        self.old_argv = sys.argv
        sys.argv = ["staging-release-controller.py", "deploy", "1", "1", WORKFLOW, V020, str(self.run)]

    def tearDown(self):
        sys.argv = self.old_argv
        self.temp.cleanup()

    def deploy_result(self, code=0, report=None):
        def execute(args, env, capture_output, text):
            evidence = self.run / "deployment-evidence.json"
            if code == 0:
                self.active.write_text(compose_text(V020), encoding="utf-8")
            if report is not None:
                evidence.write_text(json.dumps(report), encoding="utf-8")
            return type("Result", (), {"returncode": code, "stderr": ""})()
        return execute

    def report(self, **changes):
        snapshot = {
            "version": V020,
            "image_ref": IMAGE_020,
            "image_id": "sha256:" + "b" * 64,
            "schema_version": 3,
        }
        value = {
            "schema": "asteria-drive-staging-deployment/v2",
            "status": "success", "last_phase": "complete",
            "github_run_id": "1", "github_run_attempt": "1", "workflow_sha": WORKFLOW,
            "release_tag": V020, "source_commit": SOURCE_020, "image": IMAGE_020,
            "compose_sha256": self.releases[V020]["compose_sha256"],
            "deployed_image_ref": snapshot["image_ref"], "deployed_image_id": snapshot["image_id"],
            "migration_schema_after": 3,
            "health_succeeded": True, "readiness_succeeded": True,
        }
        value.update(changes)
        return value

    def invoke(self, result=None):
        result = result or self.deploy_result(report=self.report())
        with mock.patch.object(controller.subprocess, "run", side_effect=result):
            return self.run_controller()

    def run_controller(self, runtime=_MISSING):
        selected = self.runtime if runtime is _MISSING else runtime
        return controller.main(
            selected,
            deploy_root=self.root,
            state_path=self.state,
            releases_path=self.manifest,
        )

    def state_value(self):
        return json.loads(self.state.read_text(encoding="utf-8"))

    def assert_recovery(self):
        self.assertEqual(1, self.invoke())
        self.assertEqual(controller.RECOVERY_REQUIRED, self.state_value()["state"])

    def test_success_active_and_exact_replay(self):
        self.assertEqual(0, self.invoke())
        value = self.state_value()
        self.assertEqual(controller.ACTIVE, value["state"])
        self.assertEqual(V020, value["active_version"])
        self.assertEqual(3, value["schema_version"])
        self.assertEqual(0, self.invoke())
        self.assertGreaterEqual(self.runtime.calls.count(V020), 2)

    def test_main_constructs_default_runtime_verifier(self):
        with mock.patch.object(controller, "RuntimeVerifier", return_value=self.runtime) as factory, \
                mock.patch.object(controller.subprocess, "run", side_effect=self.deploy_result(report=self.report())):
            self.assertEqual(0, self.run_controller(None))
        factory.assert_called_once_with(self.root)

    def test_environment_path_overrides_are_ignored(self):
        invoked = self.root / "invoked-state.json"
        invoked_manifest = self.root / "invoked-manifest.json"
        with mock.patch.dict(os.environ, {
            "ASTERIA_CONTROLLER_STATE": str(invoked),
            "ASTERIA_CONTROLLER_RELEASES": str(invoked_manifest),
        }):
            self.assertEqual(0, self.invoke())
        self.assertTrue(self.state.exists())
        self.assertFalse(invoked.exists())
        self.assertFalse(invoked_manifest.exists())

    def test_compose_changed_but_old_image(self):
        self.assertEqual(0, self.invoke())
        self.runtime.image_drift = True
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()

    def test_api_stopped(self):
        self.assertEqual(0, self.invoke())
        self.runtime.stopped = True
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()

    def test_schema_mismatch(self):
        self.assertEqual(0, self.invoke())
        self.runtime.schema = 2
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()

    def test_api_not_ready(self):
        self.assertEqual(0, self.invoke())
        self.runtime.ready = False
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()

    def test_pre_switch_drift_is_blocked_without_executor(self):
        self.runtime.image_drift = True
        with mock.patch.object(controller.subprocess, "run") as execute:
            with self.assertRaisesRegex(SystemExit, "1"):
                self.run_controller()
        execute.assert_not_called()

    def _post_deploy_fault(self, mutate):
        report = self.report()
        def execute(args, env, capture_output, text):
            self.active.write_text(compose_text(V020), encoding="utf-8")
            mutate()
            (self.run / "deployment-evidence.json").write_text(json.dumps(report), encoding="utf-8")
            return type("Result", (), {"returncode": 0, "stderr": ""})()
        with mock.patch.object(controller.subprocess, "run", side_effect=execute):
            self.assertEqual(1, self.run_controller())
        self.assertEqual(controller.RECOVERY_REQUIRED, self.state_value()["state"])

    def test_compose_switched_but_old_container_image(self):
        self._post_deploy_fault(lambda: setattr(self.runtime, "image_drift", True))

    def test_compose_correct_but_api_stopped(self):
        self._post_deploy_fault(lambda: setattr(self.runtime, "stopped", True))

    def test_compose_correct_but_schema_mismatch(self):
        self._post_deploy_fault(lambda: setattr(self.runtime, "schema", 2))

    def test_compose_correct_but_container_not_running(self):
        self._post_deploy_fault(lambda: setattr(self.runtime, "container_state", "exited"))

    def test_active_compose_drift_after_success_rejected_on_replay(self):
        self.assertEqual(0, self.invoke())
        self.active.write_text(compose_text(V020) + "# drift\n", encoding="utf-8")
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()
    def test_injected_wrong_run_id(self):
        self.assert_recovery_result(self.deploy_result(report=self.report(github_run_id="999")))

    def test_injected_wrong_source_commit(self):
        self.assert_recovery_result(self.deploy_result(report=self.report(source_commit="e" * 40)))

    def test_injected_wrong_evidence_image(self):
        self.assert_recovery_result(self.deploy_result(report=self.report(image=IMAGE_011)))

    def test_injected_wrong_evidence_compose(self):
        self.assert_recovery_result(self.deploy_result(report=self.report(compose_sha256="0" * 64)))
    def test_injected_wrong_workflow_sha(self):
        self.assert_recovery_result(self.deploy_result(report=self.report(workflow_sha="d" * 40)))

    def assert_recovery_result(self, result):
        with mock.patch.object(controller.subprocess, "run", side_effect=result):
            self.assertEqual(1, self.run_controller())
        self.assertEqual(controller.RECOVERY_REQUIRED, self.state_value()["state"])

    def test_rollback_succeeded_but_runtime_not_restored(self):
        failed = self.report(
            status="failed", last_phase="start-api", rollback_succeeded=True,
            previous_image=IMAGE_011, deployed_image_ref=IMAGE_011,
            deployed_image_id="sha256:" + "a" * 64,
        )
        def execute(args, env, capture_output, text):
            self.active.write_text(compose_text(V020), encoding="utf-8")
            (self.run / "deployment-evidence.json").write_text(json.dumps(failed), encoding="utf-8")
            return type("Result", (), {"returncode": 1, "stderr": ""})()
        with mock.patch.object(controller.subprocess, "run", side_effect=execute):
            self.assertEqual(1, self.run_controller())
        self.assertEqual(controller.RECOVERY_REQUIRED, self.state_value()["state"])

    def test_compensation_requires_runtime_reverification(self):
        failed = self.report(
            status="failed", last_phase="start-api", rollback_succeeded=True,
            previous_image=IMAGE_011, deployed_image_ref=IMAGE_011,
            deployed_image_id="sha256:" + "a" * 64,
        )
        def execute(args, env, capture_output, text):
            self.active.write_text(compose_text(V011), encoding="utf-8")
            (self.run / "deployment-evidence.json").write_text(json.dumps(failed), encoding="utf-8")
            return type("Result", (), {"returncode": 1, "stderr": ""})()
        with mock.patch.object(controller.subprocess, "run", side_effect=execute):
            self.assertEqual(1, self.run_controller())
        value = self.state_value()
        self.assertEqual(controller.ACTIVE, value["state"])
        self.assertEqual(V011, value["active_version"])
        self.assertGreaterEqual(self.runtime.calls.count(V011), 2)
    def test_compensation_accepts_early_rollback_without_deployed_image(self):
        failed = self.report(
            status="failed", last_phase="start-api", rollback_succeeded=True,
            previous_image=IMAGE_011, deployed_image_ref="none",
            deployed_image_id="none",
        )
        def execute(args, env, capture_output, text):
            self.active.write_text(compose_text(V011), encoding="utf-8")
            (self.run / "deployment-evidence.json").write_text(json.dumps(failed), encoding="utf-8")
            return type("Result", (), {"returncode": 1, "stderr": ""})()
        with mock.patch.object(controller.subprocess, "run", side_effect=execute):
            self.assertEqual(1, self.run_controller())
        value = self.state_value()
        self.assertEqual(controller.ACTIVE, value["state"])
        self.assertEqual(V011, value["active_version"])

    def test_compensation_with_wrong_previous_image_is_recovery(self):
        failed = self.report(
            status="failed", last_phase="start-api", rollback_succeeded=True,
            previous_image=IMAGE_020, deployed_image_ref="none", deployed_image_id="none",
        )
        def execute(args, env, capture_output, text):
            self.active.write_text(compose_text(V011), encoding="utf-8")
            (self.run / "deployment-evidence.json").write_text(json.dumps(failed), encoding="utf-8")
            return type("Result", (), {"returncode": 1, "stderr": ""})()
        with mock.patch.object(controller.subprocess, "run", side_effect=execute):
            self.assertEqual(1, self.run_controller())
        self.assertEqual(controller.RECOVERY_REQUIRED, self.state_value()["state"])
    def test_tampered_active_journal_rejected_on_replay(self):
        self.assertEqual(0, self.invoke())
        record = self.state_value()
        record["active_version"] = V011
        self.state.write_text(json.dumps(record), encoding="utf-8")
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()

    def test_replay_runtime_drift_rejected(self):
        self.assertEqual(0, self.invoke())
        self.runtime.image_drift = True
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()

    @unittest.skipIf(os.name == "nt", "Windows symlink requires elevation")
    def test_state_symlink_rejected(self):
        self.state.unlink(missing_ok=True)
        target = self.root / "other.json"
        target.write_text("{}", encoding="utf-8")
        self.state.symlink_to(target)
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()

    @unittest.skipIf(os.name == "nt", "Windows does not expose POSIX modes")
    def test_state_permissions_rejected(self):
        self.state.write_text("{}", encoding="utf-8")
        self.state.chmod(0o666)
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()

    @unittest.skipIf(os.name == "nt", "Windows does not expose POSIX ownership")
    def test_state_owner_mismatch_rejected_directly(self):
        self.state.write_text(json.dumps({"format": controller.FORMAT, "state": "IDLE"}), encoding="utf-8")
        self.state.chmod(0o600)
        with mock.patch.object(controller.os, "geteuid", return_value=os.geteuid() + 1):
            with self.assertRaisesRegex(SystemExit, "1"):
                controller.load_state(self.state, self.root)
    @unittest.skipUnless(os.name == "posix" and shutil.which("sudo"), "needs passwordless sudo")
    def test_state_owner_replacement_rejected(self):
        self.state.write_text(json.dumps({"format": controller.FORMAT, "state": "IDLE"}), encoding="utf-8")
        if subprocess.run(["sudo", "-n", "true"], capture_output=True).returncode != 0:
            self.skipTest("passwordless sudo is unavailable")
        subprocess.run(["sudo", "-n", "chown", "0:0", str(self.state)], check=True, capture_output=True)
        try:
            with self.assertRaisesRegex(SystemExit, "1"):
                self.invoke()
        finally:
            subprocess.run(["sudo", "-n", "rm", "-f", str(self.state)], check=False, capture_output=True)

    def test_interrupted_attempt_blocked(self):
        self.state.write_text(json.dumps({"format": controller.FORMAT, "state": controller.ATTEMPTING, "run_id": "1", "attempt": "1", "target_version": V020}), encoding="utf-8")
        self.state.chmod(0o600)
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()

    def test_recovery_required_blocks_new_operation(self):
        self.state.write_text(json.dumps({"format": controller.FORMAT, "state": controller.RECOVERY_REQUIRED, "run_id": "1", "attempt": "1", "target_version": V020}), encoding="utf-8")
        self.state.chmod(0o600)
        with self.assertRaisesRegex(SystemExit, "1"):
            self.invoke()


class RuntimeVerifierCase(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        (self.root / "compose.yaml").write_text(compose_text(V020), encoding="utf-8")
        self.bundle = {"image": IMAGE_020, "schema_version": 3}
        self.calls = []
        self.ready = True
        self.stopped = False
        self.container_state = "running"
        self.container_health = "healthy"
        self.status = "running"
        self.health = "none"
        self.schema = "3"
        self.image = IMAGE_020
        self.published = {"8080": "127.0.0.1:18080", "9090": "127.0.0.1:19090"}

    def tearDown(self):
        self.temp.cleanup()

    def runner(self, args):
        self.calls.append(args)
        if args[:2] == ["docker", "compose"] and "psql" in args:
            return 0, self.schema
        if args[:2] == ["docker", "compose"] and "port" in args:
            return 0, self.published[args[-1]]
        if args[-3:] == ["ps", "-q", "api"]:
            return (1, "") if self.stopped else (0, "api-id\n")
        if args[:2] == ["docker", "inspect"]:
            fmt = args[3]
            if fmt == "{{.Config.Image}}":
                return 0, self.image
            if fmt == "{{.Image}}":
                return 0, "sha256:" + "b" * 64
            if fmt == "{{.State.Status}}":
                return 0, self.status
            return 0, self.health
        if args[:2] == ["docker", "image"]:
            return 0, "sha256:" + "b" * 64
        raise AssertionError(args)

    def verifier(self):
        return RuntimeVerifier(self.root, runner=self.runner, curl=lambda path: self.ready)

    def test_real_adapter_calls_docker_db_and_readiness(self):
        snapshot = self.verifier().snapshot(V020, self.bundle)
        self.assertEqual(IMAGE_020, snapshot["image_ref"])
        self.assertEqual(3, snapshot["schema_version"])
        commands = [" ".join(call) for call in self.calls]
        self.assertTrue(any("ps -q api" in command for command in commands))
        self.assertTrue(any("docker image inspect" in command for command in commands))
        self.assertTrue(any("port api 8080" in command for command in commands))
        self.assertTrue(any("psql" in command for command in commands))

    def test_stopped_api_rejected(self):
        self.stopped = True
        with self.assertRaises(RuntimeVerificationError):
            self.verifier().snapshot(V020, self.bundle)

    def test_image_drift_rejected(self):
        self.image = IMAGE_011
        with self.assertRaises(RuntimeVerificationError):
            self.verifier().snapshot(V020, self.bundle)

    def test_schema_drift_rejected(self):
        self.schema = "2"
        with self.assertRaises(RuntimeVerificationError):
            self.verifier().snapshot(V020, self.bundle)

    def test_readiness_rejected(self):
        self.ready = False
        with self.assertRaises(RuntimeVerificationError):
            self.verifier().snapshot(V020, self.bundle)

    def test_unhealthy_container_rejected(self):
        self.health = "unhealthy"
        with self.assertRaises(RuntimeVerificationError):
            self.verifier().snapshot(V020, self.bundle)

    def test_port_binding_mismatch_rejected(self):
        self.published["8080"] = "0.0.0.0:18080"
        with self.assertRaises(RuntimeVerificationError):
            self.verifier().snapshot(V020, self.bundle)

    def test_command_timeout_is_verification_error(self):
        with mock.patch("staging_runtime_verifier.subprocess.run", side_effect=subprocess.TimeoutExpired("docker", 30)):
            with self.assertRaises(RuntimeVerificationError):
                RuntimeVerifier(self.root).snapshot(V020, self.bundle)


if __name__ == "__main__":
    unittest.main()
