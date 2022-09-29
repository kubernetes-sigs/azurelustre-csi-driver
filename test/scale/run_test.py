"""
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
"""
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

ROOT_PATH = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
WORK_PATH = os.path.join(os.path.join(ROOT_PATH, "test"), "scale")

logger.info(f"ROOT_PATH: {ROOT_PATH}")
logger.info(f"WORK_PATH: {WORK_PATH}")


class FuncPerfResult:
    FUNC_NAME = ""

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
    WORK_LOAD_YAML_PATH = ""
    TARGET_FUNCS = []

    def __init__(self, args):
        self.configurations = {
            "csi_name": args.csi_name,
            "mgs_ip_address": args.mgs_ip_address,
            "fs_name": args.fs_name,
            "scale": 0,
        }
        self._scales = args.scales
        logger.info(f"test scales {self._scales}")
        self._test_result = None
        self._provisioning_type = args.provisioning_type
        self._generated_workload_yaml = os.path.join(WORK_PATH,
                                                     "tmp_workload.yml")
        self._csi_log_path = os.path.join(WORK_PATH, "logs")
        self._result_path = WORK_PATH
        logger.info(f"template workload yaml {self.WORK_LOAD_YAML_PATH} "
                    f"result path {self._result_path}")

        self._current_scale = 0

    def generate_workload_yaml(self):
        with open(self.WORKLOAD_YAML_PATH, "r") as source_file, \
             open(self._generated_workload_yaml, "w") as target_file:
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
                    raise Exception(
                        f"template in '{line}' doesn't end in one line"
                    )

                target_file.write("".join(output_line_parts))
        
        logger.info("generated workload yaml:")
        self.run_command(f"cat {self._generated_workload_yaml}")
    
    def run_command(self, command: str, need_stdout=False, raise_error=True):
        logger.info(f"run command {command}")
        stdout = None
        if need_stdout:
            stdout = subprocess.PIPE
        process = subprocess.run(command, shell=True, text=True, stdout=stdout)
        if process.returncode != 0 and raise_error:
            raise RuntimeError(f"command {command} exit with error"
                               f" code {process.returncode}")
        stdout = process.stdout
        if need_stdout:
            logger.info(stdout)
        return stdout
    
    def setup(self, current_scale):
        logger.info("reinstalling CSI driver")
        self.run_command(f"{ROOT_PATH}/deploy/uninstall-driver.sh")
        self.run_command(f"{ROOT_PATH}/deploy/install-driver.sh")
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
            "kubectl wait pod"
            " --for=condition=Ready"
            " --selector=app=csi-scale-test"
            " --timeout=300s"
        )
        logger.info("workload was ready")
    
    def delete_workload(self):
        logger.info("deleting workload")
        self.run_command(
            f"kubectl delete"
            f" -f {self._generated_workload_yaml}"
            f" --ignore-not-found"
        )
        logger.info("waiting for workload to be deleted")
        self.run_command(
            "kubectl wait pod"
            " --for=delete"
            " --selector=app=csi-scale-test"
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
        logger.info(f"got controller pods {controller_pods}")
                
        node_pods = self.run_command(
            "kubectl get pods"
            " -nkube-system"
            " --selector=app=csi-azurelustre-node"
            " --no-headers"
            " | awk '{print $1}'",
            need_stdout=True
        )
        node_pods = node_pods.strip('\n').split('\n')
        logger.info(f"got nod pods {node_pods}")

        for pod in chain(controller_pods, node_pods):
            logger.info(f"collecting pod {pod}")
            self.run_command(f"kubectl logs {pod}"
                             f" -nkube-system"
                             f" -cazurelustre"
                             f" >{self._csi_log_path}/{pod}")

    
    def parse_result_from_log(self):
        logger.info("parsing log file for perf result")
        for log_file in os.listdir(self._csi_log_path):
            logger.info(f"parsing {log_file}")
            log_path = os.path.join(self._csi_log_path, log_file)
            if os.path.isfile(log_path):
                with open(log_path, "r") as f:
                    self._perf_result.parse_log_file(f)

    def write_result(self):
        result_file_path = os.path.join(self._result_path,
                                        f"result_{self._provisioning_type}"
                                        f"_{self._current_scale}")
        perf_result = json.dumps(self._perf_result.get_result())
        logger.info(f"perf result for scale {self._current_scale}:")
        logger.info(perf_result)
        with open(result_file_path, "w") as f:
            f.write(perf_result)
        logger.info(f"result have been wrote to {result_file_path}")

    def clean_up(self):
        logger.info("cleaning")
        self.delete_workload()
        if os.path.exists(self._csi_log_path):
            logger.info("deleting log folder")
            shutil.rmtree(self._csi_log_path)
        if os.path.exists(self._generated_workload_yaml):
            logger.info("deleting tmp yaml file")
            os.remove(self._generated_workload_yaml)

    def run(self):        
        for scale in self._scales:
            logger.info(f"run scale test {scale}")
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


class StaticPerfScaleTest(PerfScaleTest):
    WORKLOAD_YAML_PATH = os.path.join(WORK_PATH,
                                     "static_workload.yml.template")
    TARGET_FUNCS = [
        FuncNodePublishVolume(),
        FuncNodeUnpublishVolume()
    ]



def run_scale_test(args):
    logger.info(f"test csi perf for {args.provisioning_type} provisioning")
    if args.provisioning_type == "static":    
        test_class = StaticPerfScaleTest(args)
    else:
        raise NotImplemented(args.provisioning_type)
    
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
