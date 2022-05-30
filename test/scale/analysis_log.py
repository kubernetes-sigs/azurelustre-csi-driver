import json
from datetime import datetime
import os
import re


class FuncLogs:
    FUNC_NAME = ""

    def __init__(self) -> None:
        self.consumed_time = 0
        self.begin_time = 0
        self.end_time = 0
        self._serialized_data
    
    def pack(self):
        self.consumed_time = self.end_time - self.consumed_time
        self._serialized_data = {
            "function_name": self.FUNC_NAME,
            "begin_time": self.begin_time,
            "end_time": self.end_time,
            "consumed_time": self.consumed_time
        }
        self._serialized_data = json.dumps(self._serialized_data)
    
    def get_serialized_data(self):
        return self._serialized_data


class FuncControllerCreateVolume(FuncLogs):
    FUNC_NAME = "ControllerCreateVolume"


class FuncControllerDeleteVolume(FuncLogs):
    FUNC_NAME = "ControllerDeleteVolume"


class FuncControllerPublishVolume(FuncLogs):
    FUNC_NAME = "ControllerPublishVolume"


class FuncControllerUnpublishVolume(FuncLogs):
    FUNC_NAME = "ControllerUnpublishVolume"


class FuncNodePublishVolume(FuncLogs):
    FUNC_NAME = "NodePublishVolume"


class NodeUnpublishVolume(FuncLogs):
    FUNC_NAME = "NodeUnpublishVolume"


class ConsumedTimeStatistic:
    def __init__(self) -> None:
        self.times = []
        self.max_time = 0
        self.min_time = 2 ** 64
        self.total_time = 0
        self.mean_time = 0
    
    def add_time(self, consumed_time):
        self.times.append(consumed_time)
        if consumed_time < self.min_time:
            self.min_time = consumed_time
        if consumed_time > self.max_time:
            self.max_time = consumed_time
        self.total_time += consumed_time
        self.mean_time = self.total_time // len(self.times)


class ServiceLogsPerPod:
    def __init__(self, pod_name) -> None:
        self.pod_name = pod_name
        self.func_logs = {}
        self.func_time_statistic = {}
        self.consumed_time_statistic = ConsumedTimeStatistic()
        self._serialized_data = None

    def add_func_log(self, func_logs: FuncLogs) -> None:
        if func_logs.FUNC_NAME not in self.func_logs:
            self.func_logs.setdefault(func_logs.FUNC_NAME, [])
            self.func_time_statistic.setdefault(func_logs.FUNC_NAME,
                                                ConsumedTimeStatistic)
        self.func_logs[func_logs.FUNC_NAME].append(func_logs)
        self.func_time_statistic[func_logs.FUNC_NAME].add_time(
            func_logs.consumed_time
        )

    def pack(self):
        for func_name, time_statistic in self.func_time_statistic.items():
            self.consumed_time_statistic.add_time(time_statistic.total_time)
        self._serialized_data = {
            "total_time": self.consumed_time_statistic.total_time
        }
        self._serialized_data = json.dumps(self._serialized_data)

    def get_serialized_data(self):
        return self._serialized_data


class ServiceLogs:
    SERVICE_NAME = ""
    LOG_FOLDER_NAME = ""

    
class ServiceController(ServiceLogs):
    SERVICE_NAME = "ControllerService"
    LOG_FOLDER_NAME = "csi_controller"


class ServiceNode(ServiceLogs):
    SERVICE_NAME = "NodeService"
    LOG_FOLDER_NAME = "csi_node"


class TestTimeLogs:
    def __init__(self) -> None:
        self.begin_test_time = None
        self.pods_ready_time = None
        self.begin_delete_time = None
        self.end_delete_time = None
        self.end_test_time = None
        
        self.total_test_time = None
        self.total_pod_create_time = None


def analysis_node_service_log(log_file_path):
    result_data = {
        "node_publish_time": 0,
        "node_publish_num": 0,
        "node_unpublish_time": 0,
        "node_unpublish_num": 0,
    }
    with open(log_file_path, "r") as log_file:
        for line in log_file:
            line = line.strip()
            if line.find("Observed Request Latency") != -1:
                latency_seconds = float(re.search(r"latency_seconds=([0-9.e-]*)", line).group(1))
                if line.find("node_publish_volume") != -1:
                    statistic_key = "node_publish_time"
                    result_data["node_publish_num"] += 1
                elif line.find("node_unpublish_volume") != -1:
                    statistic_key = "node_unpublish_time"            
                    result_data["node_unpublish_num"] += 1
                if statistic_key is not None:
                    result_data[statistic_key] += latency_seconds
    return result_data


def analysis_controller_service_log(log_file_path):
    result_data = {
        "create_volume_time": 0,
        "delete_volume_time": 0
    }
    with open(log_file_path, "r") as log_file:
        for line in log_file:
            line = line.strip()
            if line.find("Observed Request Latency") != -1:
                latency_seconds = float(re.search(r"latency_seconds=([0-9.e-]*)", line).group(1))
                if line.find("controller_create_volume") != -1:
                    statistic_key = "create_volume_time"
                elif line.find("controller_delete_volume") != -1:
                    statistic_key = "delete_volume_time"            
                if statistic_key is not None:
                    result_data[statistic_key] += latency_seconds
        
    return result_data


LOG_PATH = os.path.join(os.path.dirname(__file__), "logs")


if __name__ == "__main__":
    result = {
        "total_test_time": 0,
        "pods_setup_time": 0,
        "pods_delete_time": 0,
        "create_volume_time": 0,
        "node_publish_time": 0,
        "node_publish_num": 0,
        "node_unpublish_time": 0,
        "node_unpublish_num": 0,
        "delete_volume_time": 0,
    }

    with open(os.path.join(LOG_PATH, "test_time"), "r") as test_time_file:
        begin_test_time = test_time_file.readline().strip().split(": ")[-1]
        begin_test_time = datetime.strptime(begin_test_time, "%Y-%m-%d %H:%M:%S.%f")
        pods_ready_time = test_time_file.readline().strip().split(": ")[-1]
        pods_ready_time = datetime.strptime(pods_ready_time, "%Y-%m-%d %H:%M:%S.%f")
        begin_delete_time = test_time_file.readline().strip().split(": ")[-1]
        begin_delete_time = datetime.strptime(begin_delete_time, "%Y-%m-%d %H:%M:%S.%f")
        end_delete_time = test_time_file.readline().strip().split(": ")[-1]
        end_delete_time = datetime.strptime(end_delete_time, "%Y-%m-%d %H:%M:%S.%f")
        end_test_time = test_time_file.readline().strip().split(": ")[-1]
        end_test_time = datetime.strptime(end_test_time, "%Y-%m-%d %H:%M:%S.%f")
    result["total_test_time"] = (end_test_time - begin_test_time).seconds
    result["pods_setup_time"] = (pods_ready_time - begin_test_time).seconds
    result["pods_delete_time"] = (end_delete_time - begin_delete_time).seconds
    
    controller_log_path = os.path.join(LOG_PATH, "csi_controller")
    for file_name in os.listdir(controller_log_path):
        file_path = os.path.join(controller_log_path, file_name)
        one_controller_result = analysis_controller_service_log(file_path)
        result["create_volume_time"] += one_controller_result["create_volume_time"]
        result["delete_volume_time"] += one_controller_result["delete_volume_time"]
    
    node_log_path = os.path.join(LOG_PATH, "csi_node")
    for file_name in os.listdir(node_log_path):
        print(file_name)
        file_path = os.path.join(node_log_path, file_name)
        one_node_result = analysis_node_service_log(file_path)
        print(one_node_result)
        result["node_publish_time"] += one_node_result["node_publish_time"]
        result["node_publish_num"] += one_node_result["node_publish_num"]
        result["node_unpublish_time"] += one_node_result["node_unpublish_time"]
        result["node_unpublish_num"] += one_node_result["node_unpublish_num"]
    print(json.dumps(result, indent=4))
