# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import run_test


class ScaleCommandTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="csi scale & ")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        logger_patch = mock.patch.object(run_test, "logger")
        logger_patch.start()
        self.addCleanup(logger_patch.stop)
        args = SimpleNamespace(
            csi_name="azurelustre.csi.azure.com",
            mgs_ip_address="192.0.2.1",
            fs_name="lustrefs",
            scales=[1],
            provisioning_type="static",
        )
        self.harness = run_test.PerfScaleTest(args)
        self.harness._generated_workload_yaml = str(
            self.root / "workload ; $(literal) [1].yaml"
        )
        self.harness._csi_log_path = str(self.root / "logs & [1]")

    def test_run_command_keeps_arguments_literal(self):
        command = ["kubectl", "apply", "-f",
                   self.harness._generated_workload_yaml]
        completed = subprocess.CompletedProcess(command, 0, stdout="output\n")
        with mock.patch.object(run_test.subprocess, "run",
                               return_value=completed) as execute:
            output = self.harness.run_command(command, need_stdout=True)

        self.assertEqual(output, "output\n")
        execute.assert_called_once_with(
            command, shell=False, text=True, stdout=subprocess.PIPE,
            check=False,
        )

    def test_setup_keeps_script_paths_literal(self):
        with mock.patch.object(run_test, "ROOT_PATH", str(self.root)), \
                mock.patch.object(self.harness, "run_command") as execute:
            self.harness.setup(1)

        self.assertEqual(execute.call_args_list, [
            mock.call([str(self.root / "deploy/uninstall-driver.sh")]),
            mock.call([str(self.root / "deploy/install-driver.sh"), "local"]),
        ])

    def test_workload_commands_keep_paths_literal(self):
        template = self.root / "template.yaml"
        template.write_text("kind: Pod\n", encoding="utf-8")
        self.harness.WORKLOAD_YAML_PATH = str(template)
        path = self.harness._generated_workload_yaml
        with mock.patch.object(self.harness, "run_command") as execute:
            self.harness.generate_workload_yaml()
            self.harness.deploy_workload()
            self.harness.delete_workload()

        self.assertEqual(execute.call_args_list, [
            mock.call(["cat", path]),
            mock.call(["kubectl", "apply", "-f", path]),
            mock.call(["kubectl", "rollout", "status", "deployment",
                       "scale-test-set", "--timeout=600s"]),
            mock.call(["kubectl", "delete", "-f", path, "--ignore-not-found",
                       "--wait=true", "--timeout=600s"]),
        ])

    def test_failed_command_preserves_retries_and_error(self):
        command = ["kubectl", "get", "pods"]
        completed = subprocess.CompletedProcess(command, 7)
        with mock.patch.object(run_test.subprocess, "run",
                               return_value=completed) as execute, \
                mock.patch.object(run_test.time, "sleep") as sleep:
            with self.assertRaisesRegex(RuntimeError, "exit with error code 7"):
                self.harness.run_command(command, retries=3)

        self.assertEqual(execute.call_count, 3)
        self.assertEqual(sleep.call_args_list, [mock.call(10), mock.call(20)])

    def test_log_collection_uses_json_and_overwrites_each_retry(self):
        Path(self.harness._csi_log_path).mkdir()
        for succeeds in (True, False):
            with self.subTest(succeeds=succeeds):
                attempts = {}
                final_attempt = 2 if succeeds else 5

                def execute(command, **kwargs):
                    self.assertIsInstance(command, list)
                    self.assertFalse(kwargs["shell"])
                    if command[:3] == ["kubectl", "get", "pods"]:
                        self.assertEqual(command[-2:], ["-o", "json"])
                        component = ("controller" if
                                     "--selector=app=csi-azurelustre-controller"
                                     in command else "node")
                        output = json.dumps({
                            "items": [{"metadata": {"name": component + "-0"}}]
                        })
                        return subprocess.CompletedProcess(command, 0,
                                                           stdout=output)
                    pod = command[2]
                    self.assertEqual(command, [
                        "kubectl", "logs", pod, "-nkube-system", "-cazurelustre",
                    ])
                    attempts[pod] = attempts.get(pod, 0) + 1
                    output = kwargs["stdout"]
                    self.assertEqual(Path(output.name),
                                     Path(self.harness._csi_log_path) / pod)
                    if attempts[pod] < final_attempt:
                        output.write("partial log that must be discarded\n")
                        return subprocess.CompletedProcess(command, 1)
                    output.write("final log\n")
                    return subprocess.CompletedProcess(
                        command, 0 if succeeds else 1,
                    )

                with mock.patch.object(run_test.subprocess, "run",
                                       side_effect=execute), \
                        mock.patch.object(run_test.time, "sleep"):
                    self.harness.collection_logs()

                self.assertEqual(attempts, {
                    "controller-0": final_attempt,
                    "node-0": final_attempt,
                })
                for pod in attempts:
                    path = Path(self.harness._csi_log_path) / pod
                    self.assertEqual(path.read_text(encoding="utf-8"),
                                     "final log\n")

    def test_log_file_open_failure_does_not_stop_collection(self):
        log_path = Path(self.harness._csi_log_path)
        log_path.mkdir()
        (log_path / "controller-0").mkdir()

        def execute(command, **kwargs):
            if command[:3] == ["kubectl", "get", "pods"]:
                component = ("controller" if
                             "--selector=app=csi-azurelustre-controller"
                             in command else "node")
                output = json.dumps({
                    "items": [{"metadata": {"name": component + "-0"}}]
                })
                return subprocess.CompletedProcess(command, 0, stdout=output)
            self.assertEqual(command[2], "node-0")
            kwargs["stdout"].write("node log\n")
            return subprocess.CompletedProcess(command, 0)

        with mock.patch.object(run_test.subprocess, "run",
                               side_effect=execute) as execute_mock:
            self.harness.collection_logs()

        self.assertEqual(execute_mock.call_count, 3)
        self.assertEqual((log_path / "node-0").read_text(encoding="utf-8"),
                         "node log\n")
        run_test.logger.warning.assert_called_once()
        self.assertIn("controller-0", run_test.logger.warning.call_args.args)
        self.assertIn(str(log_path / "controller-0"),
                      run_test.logger.warning.call_args.args)

    def test_log_file_reset_failure_does_not_stop_collection(self):
        for operation in ("seek", "truncate"):
            with self.subTest(operation=operation):
                run_test.logger.warning.reset_mock()
                broken_file = mock.MagicMock()
                broken_file.__enter__.return_value = broken_file
                getattr(broken_file, operation).side_effect = OSError(
                    "cannot reset log"
                )
                good_file = mock.MagicMock()
                good_file.__enter__.return_value = good_file
                controller_pods = json.dumps({
                    "items": [
                        {"metadata": {"name": "controller-0"}},
                        {"metadata": {"name": "controller-1"}},
                    ]
                })
                results = [
                    subprocess.CompletedProcess([], 0, stdout=controller_pods),
                    subprocess.CompletedProcess([], 0, stdout='{"items": []}'),
                    subprocess.CompletedProcess([], 0),
                ]

                with mock.patch.object(
                    run_test, "open", create=True,
                    side_effect=[broken_file, good_file],
                ), mock.patch.object(
                    run_test.subprocess, "run", side_effect=results,
                ) as execute:
                    self.harness.collection_logs()

                self.assertEqual(execute.call_count, 3)
                self.assertEqual(execute.call_args, mock.call(
                    ["kubectl", "logs", "controller-1", "-nkube-system",
                     "-cazurelustre"],
                    shell=False, text=True, stdout=good_file, check=False,
                ))
                run_test.logger.warning.assert_called_once()
                self.assertIn("controller-0",
                              run_test.logger.warning.call_args.args)

    def test_empty_pod_lists_do_not_request_logs(self):
        Path(self.harness._csi_log_path).mkdir()
        completed = subprocess.CompletedProcess([], 0, stdout='{"items": []}')
        with mock.patch.object(run_test.subprocess, "run",
                               return_value=completed) as execute:
            self.harness.collection_logs()

        self.assertEqual(execute.call_count, 2)
        self.assertEqual(list(Path(self.harness._csi_log_path).iterdir()), [])

    def test_failed_pod_discovery_stops_before_requesting_logs(self):
        completed = subprocess.CompletedProcess([], 1, stdout="")
        with mock.patch.object(run_test.subprocess, "run",
                               return_value=completed) as execute, \
                mock.patch.object(run_test.time, "sleep"):
            with self.assertRaisesRegex(RuntimeError, "exit with error code 1"):
                self.harness.collection_logs()

        self.assertEqual(execute.call_count, 5)
        for call in execute.call_args_list:
            self.assertEqual(call.args[0][:3], ["kubectl", "get", "pods"])


if __name__ == "__main__":
    unittest.main()
