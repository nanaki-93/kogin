from __future__ import annotations

import json
import os
import stat
import sys
import tempfile
import unittest
from dataclasses import replace
from pathlib import Path
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import insight_runtime as runtime  # noqa: E402


class InsightRuntimeTest(unittest.TestCase):
    def test_runtime_conformance_is_explicitly_opt_in(self) -> None:
        candidate = os.environ.get("KOGIN_INSIGHT_RUNTIME_CANDIDATE")
        if not candidate:
            self.skipTest("set KOGIN_INSIGHT_RUNTIME_CANDIDATE to run pinned-runtime conformance")
        candidate_dir = Path(candidate).resolve()
        python = candidate_dir / "venv/bin/python"
        if not python.is_file():
            self.fail(f"pinned runtime Python is unavailable: {python}")
        helper = SCRIPTS / "tests/insight_runtime_conformance.py"
        environment = {"PATH": os.environ.get("PATH", ""), "HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "PYTHONDONTWRITEBYTECODE": "1"}
        try:
            completed = runtime.subprocess.run(
                [str(python), str(helper), "--candidate-dir", str(candidate_dir), "--source-root", str(SCRIPTS.parent)],
                capture_output=True,
                check=False,
                text=True,
                env=environment,
                timeout=30,
            )
        except runtime.subprocess.TimeoutExpired:
            self.fail("pinned runtime conformance timed out after 30 seconds")
        if completed.returncode:
            self.fail(f"pinned runtime conformance failed: {completed.stderr[-1000:]}")
        self.assertIn('"status": "passed"', completed.stdout)

    def config(self, directory: Path) -> runtime.RuntimeConfig:
        return runtime.RuntimeConfig(
            candidate_dir=directory,
            python=Path(sys.executable),
            python_version="3.11.16",
            port=1235,
            model="./models/qwen38-v12-thinking-schema-1",
            artifact=directory / "artifact",
            artifact_identity_hash="identity",
            artifact_identity_file=directory / "artifact-identity.json",
            artifact_identity_sha256="artifact-record",
            audit_file=directory / "runtime-compatibility.json",
            audit_sha256="audit",
            requirements_file=directory / "resolved-requirements.txt",
            requirements_sha256="requirements",
            state_dir=directory / "runtime",
            distributions={"mlx-vlm": "0.7.0"},
            source_hashes={"app.py": "source"},
            readiness_timeout_seconds=1,
        )

    def test_child_environment_is_fresh_and_fixed(self) -> None:
        environment = runtime.child_environment({"PATH": "/bin", "HF_HUB_OFFLINE": "0", "KV_BITS": "4"})
        self.assertEqual({"PATH", *runtime.FIXED_ENVIRONMENT}, set(environment))
        self.assertEqual("1", environment["HF_HUB_OFFLINE"])
        self.assertNotIn("KV_BITS", environment)

    def test_read_config_rejects_unsupported_request_setting(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            required = directory / "resolved-requirements.txt"
            required.write_text("mlx-vlm==0.7.0 --hash=sha256:abc\n", encoding="utf-8")
            config = {
                "schema_version": 1, "host": "127.0.0.1", "port": 1235,
                "python": "venv/bin/python", "python_version": "3.11.16",
                "wire_model": runtime.REQUIRED_REQUEST["model"], "artifact_realpath": "/tmp/model",
                "artifact_realpath_sha256": "x", "artifact_identity_file": "artifact-identity.json",
                "audit_file": "runtime-compatibility.json", "audit_sha256": "x",
                "requirements_file": "resolved-requirements.txt", "requirements_sha256": "x",
                "state_dir": "runtime", "distributions": {"mlx-vlm": "0.7.0"},
                "server_source_sha256": {"app.py": "x"}, "environment": runtime.FIXED_ENVIRONMENT,
                "max_num_seqs": 1, "thinking": True, "speculative_decoding": False, "apc": False,
                "request": {**runtime.REQUIRED_REQUEST, "repeat_penalty": 1},
            }
            config_path = directory / "runtime.json"
            config_path.write_text(json.dumps(config), encoding="utf-8")
            config_path.chmod(0o600)
            with self.assertRaisesRegex(runtime.RuntimeError, "request must exactly"):
                runtime.read_config(directory)

    def test_read_config_accepts_only_the_fixed_recovery_profile(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            required = directory / "resolved-requirements.txt"
            required.write_text("mlx-vlm==0.7.0 --hash=sha256:abc\n", encoding="utf-8")
            request = runtime.FIXED_REQUESTS["./models/qwen38-v13-recovery-1"]
            config = {
                "schema_version": 1, "host": "127.0.0.1", "port": 1235,
                "python": "venv/bin/python", "python_version": "3.11.16",
                "wire_model": request["model"], "artifact_realpath": "/tmp/model",
                "artifact_realpath_sha256": "x", "artifact_identity_file": "artifact-identity.json",
                "artifact_identity_sha256": "x", "audit_file": "runtime-compatibility.json", "audit_sha256": "x",
                "requirements_file": "resolved-requirements.txt", "requirements_sha256": "x",
                "state_dir": "runtime", "distributions": {"mlx-vlm": "0.7.0"},
                "server_source_sha256": {"app.py": "x"}, "environment": runtime.FIXED_ENVIRONMENT,
                "max_num_seqs": 1, "thinking": True, "speculative_decoding": False, "apc": False,
                "request": request,
            }
            config_path = directory / "runtime.json"
            config_path.write_text(json.dumps(config), encoding="utf-8")
            config_path.chmod(0o600)
            self.assertEqual(request["model"], runtime.read_config(directory).model)
            config["request"] = {**request, "reasoning_effort": "xhigh"}
            config_path.write_text(json.dumps(config), encoding="utf-8")
            with self.assertRaisesRegex(runtime.RuntimeError, "request must exactly"):
                runtime.read_config(directory)
            config["request"] = {**request, "max_tokens": 4095}
            config_path.write_text(json.dumps(config), encoding="utf-8")
            with self.assertRaisesRegex(runtime.RuntimeError, "request must exactly"):
                runtime.read_config(directory)
            config["wire_model"] = "./models/qwen38-unapproved-1"
            config["request"] = {**request, "model": config["wire_model"]}
            config_path.write_text(json.dumps(config), encoding="utf-8")
            with self.assertRaisesRegex(runtime.RuntimeError, "approved fixed candidate"):
                runtime.read_config(directory)

    def test_read_config_rejects_environment_drift(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            required = directory / "resolved-requirements.txt"
            required.write_text("mlx-vlm==0.7.0 --hash=sha256:abc\n", encoding="utf-8")
            config = {
                "schema_version": 1, "host": "127.0.0.1", "port": 1235,
                "python": "venv/bin/python", "python_version": "3.11.16",
                "wire_model": runtime.REQUIRED_REQUEST["model"], "artifact_realpath": "/tmp/model",
                "artifact_realpath_sha256": "x", "artifact_identity_file": "artifact-identity.json",
                "artifact_identity_sha256": "x", "audit_file": "runtime-compatibility.json", "audit_sha256": "x",
                "requirements_file": "resolved-requirements.txt", "requirements_sha256": "x",
                "state_dir": "runtime", "distributions": {"mlx-vlm": "0.7.0"},
                "server_source_sha256": {"app.py": "x"}, "environment": {**runtime.FIXED_ENVIRONMENT, "KV_BITS": "4"},
                "max_num_seqs": 1, "thinking": True, "speculative_decoding": False, "apc": False,
                "request": runtime.REQUIRED_REQUEST,
            }
            config_path = directory / "runtime.json"
            config_path.write_text(json.dumps(config), encoding="utf-8")
            config_path.chmod(0o600)
            with self.assertRaisesRegex(runtime.RuntimeError, "environment"):
                runtime.read_config(directory)

    def test_check_is_read_only_and_rejects_occupied_port(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config = self.config(Path(temporary))
            config.requirements_file.write_text("mlx-vlm==0.7.0 --hash=sha256:abc\n", encoding="utf-8")
            config = runtime.RuntimeConfig(**{**config.__dict__, "requirements_sha256": runtime.sha256_file(config.requirements_file)})
            with mock.patch.object(runtime, "verify_audit"), mock.patch.object(runtime, "verify_model_link"), \
                    mock.patch.object(runtime, "verify_source_hashes"), mock.patch.object(runtime, "run_metadata"), \
                    mock.patch.object(runtime, "port_is_free", return_value=False), \
                    mock.patch.object(runtime, "TRACKED_REQUIREMENTS_FILE", config.requirements_file), \
                    mock.patch.object(runtime.subprocess, "Popen") as popen:
                with self.assertRaisesRegex(runtime.RuntimeError, "occupied"):
                    runtime.check(config)
                popen.assert_not_called()
            self.assertFalse(config.state_dir.exists())

    def test_missing_dependency_metadata_fails_without_importing_runtime(self) -> None:
        with self.assertRaisesRegex(runtime.RuntimeError, "not executable"):
            runtime.run_metadata(Path("/does/not/exist"), "3.11.16", {"mlx-vlm": "0.7.0"})

    def test_metadata_rejects_missing_or_wrong_pinned_dependency(self) -> None:
        completed = mock.Mock(stdout='{"python":"3.11.16","distributions":{"mlx-vlm":"0.6.5"}}')
        with mock.patch.object(runtime.subprocess, "run", return_value=completed):
            with self.assertRaisesRegex(runtime.RuntimeError, "distribution versions drifted"):
                runtime.run_metadata(Path(sys.executable), "3.11.16", {"mlx-vlm": "0.7.0"})

    def test_lms_unrecognized_output_fails_closed(self) -> None:
        completed = mock.Mock(stdout='{"unexpected": []}', returncode=0)
        with mock.patch.object(runtime.subprocess, "run", return_value=completed):
            with self.assertRaisesRegex(runtime.RuntimeError, "unrecognized"):
                runtime.require_lms_empty()

    def test_process_inspection_failure_keeps_state_fail_closed(self) -> None:
        with mock.patch.object(runtime.subprocess, "run", side_effect=runtime.subprocess.TimeoutExpired(["ps"], 1)):
            with self.assertRaisesRegex(runtime.RuntimeError, "cannot inspect"):
                runtime.process_identity(4312)

    def test_lifecycle_lock_rejects_concurrent_start_or_stop(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config = self.config(Path(temporary))
            with runtime.lifecycle_lock(config):
                with self.assertRaisesRegex(runtime.RuntimeError, "operation is in progress"):
                    with runtime.lifecycle_lock(config):
                        pass

    def assert_malicious_runtime_path_is_rejected(self, config: runtime.RuntimeConfig, protected: Path) -> None:
        original = protected.read_bytes()
        original_mode = stat.S_IMODE(protected.stat().st_mode)
        with mock.patch.object(runtime.subprocess, "Popen") as popen:
            with self.assertRaisesRegex(runtime.RuntimeError, "runtime"):
                runtime.start(config)
        popen.assert_not_called()
        self.assertEqual(original, protected.read_bytes())
        self.assertEqual(original_mode, stat.S_IMODE(protected.stat().st_mode))

    def test_state_dir_rejects_expected_model_symlink_and_runtime_symlink(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            config = self.config(directory)
            artifact = directory / "artifact"
            artifact.mkdir()
            protected = artifact / "sentinel"
            protected.write_bytes(b"model bytes")
            protected.chmod(0o444)
            model_link = directory / "models" / "qwen38-v12-thinking-schema-1"
            model_link.parent.mkdir()
            model_link.symlink_to(artifact, target_is_directory=True)
            self.assert_malicious_runtime_path_is_rejected(replace(config, state_dir=model_link), protected)
            (directory / "runtime").symlink_to(artifact, target_is_directory=True)
            self.assert_malicious_runtime_path_is_rejected(config, protected)

    def test_runtime_mutable_leaf_symlinks_do_not_escape(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            config = self.config(directory)
            config.state_dir.mkdir(mode=0o700)
            protected = directory / "outside"
            protected.write_bytes(b"outside bytes")
            protected.chmod(0o444)
            for name in runtime.MUTABLE_RUNTIME_FILES:
                leaf = config.state_dir / name
                leaf.symlink_to(protected)
                self.assert_malicious_runtime_path_is_rejected(config, protected)
                leaf.unlink()

    def test_health_rejects_context_drift_and_extra_lane(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config = self.config(Path(temporary))
            health = {
                "status": "healthy", "loaded_model": config.model, "configured_context_limit": 119552,
                "effective_context_limit": 119552, "continuous_batching_enabled": True, "apc_enabled": False,
                "loaded_models": {"text_generation": {"model": config.model}, "embedding": {"model": "other"}},
            }
            with self.assertRaisesRegex(runtime.RuntimeError, "extra model lane"):
                runtime.verify_health(config, health)

    def test_health_transport_disables_proxies_and_refuses_redirects(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config = self.config(Path(temporary))
            response = mock.MagicMock(status=200)
            response.read.return_value = b"{}"
            opener = mock.MagicMock()
            opener.open.return_value.__enter__.return_value = response
            with mock.patch.object(runtime.urllib.request, "build_opener", return_value=opener) as build:
                self.assertEqual({}, runtime.fetch_health(config))
            handlers = build.call_args.args
            proxy = next(handler for handler in handlers if isinstance(handler, runtime.urllib.request.ProxyHandler))
            self.assertEqual({}, proxy.proxies)
            redirect = next(handler for handler in handlers if isinstance(handler, runtime.NoRedirect))
            self.assertIsNone(redirect.redirect_request(None, None, 302, "Found", None, "http://other"))

    def test_listener_owner_rejects_mismatch_and_missing_tool(self) -> None:
        mismatch = mock.Mock(returncode=0, stdout="p999\nn127.0.0.1:1235\n")
        with mock.patch.object(runtime.subprocess, "run", return_value=mismatch):
            with self.assertRaisesRegex(runtime.RuntimeError, "not owned"):
                runtime.verify_listener_owner(4321, 1235)
        with mock.patch.object(runtime.subprocess, "run", side_effect=FileNotFoundError):
            with self.assertRaisesRegex(runtime.RuntimeError, "cannot inspect"):
                runtime.verify_listener_owner(4321, 1235)

    def test_stop_stale_pid_removes_state_without_signalling(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config = self.config(Path(temporary))
            config.state_dir.mkdir(mode=0o700)
            state = {"pid": 4312, "start_identity": "old"}
            runtime.write_private_json(config, state)
            with mock.patch.object(runtime, "process_identity", return_value="new"), mock.patch.object(runtime.os, "kill") as kill:
                result = runtime.stop(config)
            self.assertEqual("stale_state_removed", result["status"])
            kill.assert_not_called()
            self.assertFalse(runtime.state_path(config).exists())

    def test_start_failure_reaps_state_created_child_and_removes_state(self) -> None:
        class FakeProcess:
            pid = 4321
            returncode = None
            terminate = mock.Mock()
            kill = mock.Mock()
            wait = mock.Mock(return_value=-15)

            def poll(self) -> None:
                return None

        with tempfile.TemporaryDirectory() as temporary:
            config = self.config(Path(temporary))
            with mock.patch.object(runtime, "check"), mock.patch.object(runtime, "require_lms_empty"), \
                    mock.patch.object(runtime.subprocess, "Popen", return_value=FakeProcess()), \
                    mock.patch.object(runtime, "process_identity", return_value="identity"), \
                    mock.patch.object(runtime, "verify_listener_owner", side_effect=runtime.RuntimeError("mismatch")):
                with self.assertRaisesRegex(runtime.RuntimeError, "mismatch"):
                    runtime.start(config)
            FakeProcess.terminate.assert_called_once()
            FakeProcess.wait.assert_called_once_with(timeout=runtime.SHUTDOWN_TIMEOUT_SECONDS)
            self.assertFalse(runtime.state_path(config).exists())
            self.assertEqual(0o600, stat.S_IMODE(runtime.log_path(config).stat().st_mode))

    def test_keyboard_interrupt_reaps_state_created_child_and_preserves_interrupt(self) -> None:
        class FakeProcess:
            pid = 4321
            returncode = None
            terminate = mock.Mock()
            kill = mock.Mock()
            wait = mock.Mock(return_value=-15)

            def poll(self) -> None:
                return None

        with tempfile.TemporaryDirectory() as temporary:
            config = self.config(Path(temporary))
            with mock.patch.object(runtime, "check"), mock.patch.object(runtime, "require_lms_empty"), \
                    mock.patch.object(runtime.subprocess, "Popen", return_value=FakeProcess()), \
                    mock.patch.object(runtime, "process_identity", return_value="identity"), \
                    mock.patch.object(runtime, "verify_listener_owner", side_effect=KeyboardInterrupt):
                with self.assertRaises(KeyboardInterrupt):
                    runtime.start(config)
            FakeProcess.terminate.assert_called_once()
            FakeProcess.wait.assert_called_once_with(timeout=runtime.SHUTDOWN_TIMEOUT_SECONDS)
            self.assertFalse(runtime.state_path(config).exists())

    def test_terminate_owned_handles_process_exit_race_and_permission_error(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config = self.config(Path(temporary))
            state = {"pid": 4312, "start_identity": "owned"}
            with mock.patch.object(runtime, "process_identity", return_value="owned"), \
                    mock.patch.object(runtime.os, "kill", side_effect=ProcessLookupError):
                self.assertTrue(runtime.terminate_owned(config, state))
            with mock.patch.object(runtime, "process_identity", return_value="owned"), \
                    mock.patch.object(runtime.os, "kill", side_effect=PermissionError("denied")):
                with self.assertRaisesRegex(runtime.RuntimeError, "cannot signal owned runtime process"):
                    runtime.terminate_owned(config, state)



if __name__ == "__main__":
    unittest.main()
