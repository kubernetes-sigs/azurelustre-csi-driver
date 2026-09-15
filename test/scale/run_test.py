# Copyright 2019 The Kubernetes Authors.
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
import sys
import json
import os
import time
import shutil
import argparse
import logging
import subprocess
import re
from itertools import chain


logging.basicConfig(stream=sys.stdout, level=logging.INFO)
logger = logging.getLogger()

ROOT_PATH = os.path.dirname(os.path.dirname(
    os.path.dirname(os.path.abspath(__file__))))
WORK_PATH = os.path.join(os.path.join(ROOT_PATH, "test"), "scale")

logger.info("ROOT_PATH: %s", ROOT_PATH)
logger.info("WORK_PATH: %s", WORK_PATH)


class FuncPerfResult:
    FUNC_NAME = ""
    LATENCY_THRESHOLD = 3

    def __init__(self):
        self.latencies = []

        tmp_buf = []
        for char in self.FUNC_NAME:
            if char.isupper():
                tmp_buf.append(f"_{char.lower()}")
            else:
                tmp_buf.append(char)
        self.identity = "".join(tmp_buf)

    def reset(self):
        self.latencies = []

    def add_latency(self, latency):
        self.latencies.append(latency)

    def get_result(self):
        if len(self.latencies) == 0:
            self.latencies.append(0)
        return {
            "func_name": self.FUNC_NAME,
            "num": len(self.latencies),
            "min": min(self.latencies),
            "max": max(self.latencies),
            "avg": round(sum(self.latencies) / len(self.latencies), 4),
            "points": self.latencies
        }


class FuncControllerCreateVolume(FuncPerfResult):
    FUNC_NAME = "ControllerCreateVolume"


class FuncControllerDeleteVolume(FuncPerfResult):
    FUNC_NAME = "ControllerDeleteVolume"


class FuncControllerPublishVolume(FuncPerfResult):
    FUNC_NAME = "ControllerPublishVolume"


class FuncControllerUnpublishVolume(FuncPerfResult):
    FUNC_NAME = "ControllerUnpublishVolume"


class FuncNodePublishVolume(FuncPerfResult):
    FUNC_NAME = "NodePublishVolume"


class FuncNodeUnpublishVolume(FuncPerfResult):
    FUNC_NAME = "NodeUnpublishVolume"


class PerfResultCollector:
    def __init__(self):
        self._perf_identity = re.compile(r"latency_seconds=([0-9.e-]*)")
        self._func_data = {}

    def register_func(self, func):
        self._func_data.setdefault(func.identity, func)

    def parse_log_file(self, f):
        for line in f:
            match = self._perf_identity.search(line)
            if match is not None:
                for func_name, func in self._func_data.items():
                    if line.find(func_name) != -1:
                        func.add_latency(float(match.group(1)))
                        break

    def get_result(self):
        return {
            "func_results": [
                func.get_result() for func in self._func_data.values()
            ]
        }


class PerfScaleTest:
    WORKLOAD_YAML_PATH = ""
    TARGET_FUNCS = []

    def __init__(self, test_args):
        self.configurations = {
            "csi_name": test_args.csi_name,
            "mgs_ip_address": test_args.mgs_ip_address,
            "fs_name": test_args.fs_name,
            "scale": 0,
            "pod.metadata.name": "${pod.metadata.name}",
        }
        self._scales = test_args.scales
        logger.info("test scales %s", self._scales)
        self._test_result = None
        self._provisioning_type = test_args.provisioning_type
        self._generated_workload_yaml = os.path.join(WORK_PATH,
                                                     "tmp_workload.yml")
        self._csi_log_path = os.path.join(WORK_PATH, "logs")
        self._result_path = WORK_PATH
        logger.info("template workload yaml %s result path %s",
                    self.WORKLOAD_YAML_PATH, self._result_path)
        self._result_files = []

        self._current_scale = 0
        self._perf_result = PerfResultCollector()

    def generate_workload_yaml(self):
        with open(self.WORKLOAD_YAML_PATH, "r", encoding="utf-8") as source_file, \
                open(self._generated_workload_yaml, "w", encoding="utf-8") as target_file:
            for line in source_file:
                output_line_parts = []
                status = 0
                parameter_start = -1
                parameter_end = -1

                for idx, char in enumerate(line):
                    if status == 0:
                        if char == '$':
                            status = 1
                        else:
                            output_line_parts.append(char)
                    elif status == 1:
                        if char == '{':
                            status = 2
                        else:
                            output_line_parts.append(f"${char}")
                            status = 0
                    elif status == 2:
                        if not char.isspace():
                            parameter_start = idx
                            status = 3
                    elif status == 3:
                        if char.isspace():
                            parameter_end = idx
                            status = 4
                        elif char == '}':
                            parameter_end = idx
                            output_line_parts.append(
                                self.configurations[
                                    line[parameter_start:parameter_end]
                                ]
                            )
                            parameter_start = -1
                            status = 0
                    elif status == 4:
                        if char == '}':
                            output_line_parts.append(
                                self.configurations[
                                    line[parameter_start:parameter_end]
                                ]
                            )
                            parameter_start = -1
                            status = 0

                if status != 0:
                    raise RuntimeError(
                        f"template in '{line}' doesn't end in one line"
                    )

                target_file.write("".join(output_line_parts))

        logger.info("generated workload yaml:")
        self.run_command(f"cat {self._generated_workload_yaml}")

    def run_command(self, command: str, need_stdout=False, raise_error=True, retries=5):
        logger.info("run command %s", command)
        stdout = None
        if need_stdout:
            stdout = subprocess.PIPE
        total_retries = retries # used for calculating sleep later
        while retries > 0:
            process = subprocess.run(command, shell=True, text=True, stdout=stdout, check=False)
            retries -=1
            if process.returncode == 0:
                break
            if process.returncode != 0 and raise_error and retries == 0:
                raise RuntimeError(f"command {command} exit with error"
                                f" code {process.returncode}")
            logger.info("command %s failed, retrying attempt %s",
                        command, retries)
            time.sleep(10 * (total_retries - retries)) # sleep between retries

        stdout = process.stdout
        if need_stdout:
            logger.info(stdout)
        return stdout

    def setup(self, current_scale):
        logger.info("reinstalling CSI driver")
        self.run_command(f"{ROOT_PATH}/deploy/uninstall-driver.sh")
        self.run_command(f"{ROOT_PATH}/deploy/install-driver.sh local")
        if os.path.exists(self._csi_log_path):
            pass
        else:
            os.mkdir(self._csi_log_path)

        self._perf_result = PerfResultCollector()
        for target_func in self.TARGET_FUNCS:
            target_func.reset()
            self._perf_result.register_func(target_func)

        self._current_scale = current_scale

    def deploy_workload(self):
        logger.info("deploying workload")
        self.run_command(
            f"kubectl apply -f {self._generated_workload_yaml}"
        )
        logger.info("waiting for workload ready")
        self.run_command(
            "kubectl rollout status deployment"
            " scale-test-set"
            " --timeout=600s"
        )
        logger.info("workload was ready")

    def delete_workload(self):
        """Delete the workload.

        We can use --wait=true to wait for all pods to be deleted. Although
        wait only wait for the deployment object itself to be deleted. But PVC
        needs to wait for all pods to finish properly before it will be
        deleted.
        """
        logger.info("deleting workload")
        self.run_command(
            f"kubectl delete"
            f" -f {self._generated_workload_yaml}"
            f" --ignore-not-found"
            f" --wait=true"
            f" --timeout=600s"
        )
        logger.info("workload was deleted")

    def collection_logs(self):
        logger.info("collecting CSI log")
        controller_pods = self.run_command(
            "kubectl get pods"
            " -nkube-system"
            " --selector=app=csi-azurelustre-controller"
            " --no-headers"
            " | awk '{print $1}'",
            need_stdout=True
        )
        controller_pods = controller_pods.strip('\n').split('\n')
        logger.info("got controller pods %s", controller_pods)

        node_pods = self.run_command(
            "kubectl get pods"
            " -nkube-system"
            " --selector=app=csi-azurelustre-node"
            " --no-headers"
            " | awk '{print $1}'",
            need_stdout=True
        )
        node_pods = node_pods.strip('\n').split('\n')
        logger.info("got node pods %s", node_pods)

        for pod in chain(controller_pods, node_pods):
            logger.info("collecting pod %s", pod)
            self.run_command(f"kubectl logs {pod}"
                             f" -nkube-system"
                             f" -cazurelustre"
                             f" >{self._csi_log_path}/{pod}",
                             raise_error=False)

    def parse_result_from_log(self):
        logger.info("parsing log file for perf result")
        for log_file in os.listdir(self._csi_log_path):
            logger.info("parsing %s", log_file)
            log_path = os.path.join(self._csi_log_path, log_file)
            if os.path.isfile(log_path):
                with open(log_path, "r", encoding="utf-8") as f:
                    self._perf_result.parse_log_file(f)

    def write_result(self):
        result_file_path = os.path.join(self._result_path,
                                        f"result_{self._provisioning_type}"
                                        f"_{self._current_scale}")
        perf_result = json.dumps(self._perf_result.get_result())
        logger.info("perf result for scale %s:", self._current_scale)
        logger.info(perf_result)
        with open(result_file_path, "w", encoding="utf-8") as f:
            f.write(perf_result)
        logger.info("result has been written to %s", result_file_path)
        self._result_files.append(result_file_path)

    def clean_up(self):
        logger.info("cleaning")
        if os.path.exists(self._generated_workload_yaml):
            logger.info("deleting workload")
            self.delete_workload()
            logger.info("deleting tmp yaml file")
            os.remove(self._generated_workload_yaml)
        if os.path.exists(self._csi_log_path):
            logger.info("deleting log folder")
            shutil.rmtree(self._csi_log_path)

    def check_results(self):
        logger.info("checking result")
        latency_thresholds = {func.FUNC_NAME: func.LATENCY_THRESHOLD
                              for func in self.TARGET_FUNCS}
        logger.info("latency thresholds are %s", latency_thresholds)
        for result_file in self._result_files:
            with open(result_file, "r", encoding="utf-8") as f:
                result_data = json.load(f)
            for func_perf_result in result_data["func_results"]:
                func_name = func_perf_result["func_name"]
                if func_perf_result["max"] > latency_thresholds[func_name]:
                    raise RuntimeError(f"function {func_name} max latency "
                                       f"{func_perf_result['max']} exceeds the "
                                       f"threshold "
                                       f"{latency_thresholds[func_name]}")
        logger.info("all passed")

    def run(self):
        for scale in self._scales:
            logger.info("run scale test %s", scale)
            self.configurations["scale"] = str(scale)
            try:
                self.setup(scale)
                self.generate_workload_yaml()
                self.deploy_workload()
                time.sleep(10)
                self.delete_workload()
                self.collection_logs()
                self.parse_result_from_log()
                self.write_result()
            finally:
                self.clean_up()

        self.check_results()


class StaticPerfScaleTest(PerfScaleTest):
    WORKLOAD_YAML_PATH = os.path.join(WORK_PATH,
                                      "static_workload.yml.template")
    TARGET_FUNCS = [
        FuncNodePublishVolume(),
        FuncNodeUnpublishVolume()
    ]


def run_scale_test(test_args):
    logger.info("test csi perf for %s provisioning", test_args.provisioning_type)
    if test_args.provisioning_type == "static":
        test_class = StaticPerfScaleTest(test_args)
    else:
        raise NotImplementedError(test_args.provisioning_type)

    test_class.run()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="test CSI perf and scale")
    parser.add_argument("--provisioning-type", choices=["static", "dynamic"],
                        help="provisioning type static or dynamic",
                        required=True)
    parser.add_argument("--scales", nargs="+", help="scale list to test",
                        type=int, required=True)
    parser.add_argument("--csi-name", help="CSI driver name", required=True)
    parser.add_argument("--mgs-ip-address", type=str,
                        help="lustre mgs ip address",
                        required=True)
    parser.add_argument("--fs-name", type=str,
                        help="lustre fs name",
                        required=True)
    args = parser.parse_args()
    run_scale_test(args)
