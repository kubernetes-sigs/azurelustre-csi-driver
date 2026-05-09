#!/bin/bash

# Copyright 2020 The Kubernetes Authors.
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

set -o errexit
set -o pipefail
set -o nounset

REPO_ROOT_PATH=${REPO_ROOT_PATH:-$(git rev-parse --show-toplevel)}

pushd "$REPO_ROOT_PATH/test/long-haul/"
source ./utils.sh

export REPO_ROOT_PATH=$REPO_ROOT_PATH
export ClusterName="${aks_cluster_name}"
export ResourceGroup="${aks_resource_group}"
export PoolName="${aks_pool_name}"
export LustreFSName="${lustre_fs_name}"
export LustreFSIP="${lustre_fs_ip}"

sed -i "s/{longhaul_agentpool}/$PoolName/g;s/{lustre_fs_name}/$LustreFSName/g;s/{lustre_fs_ip}/$LustreFSIP/g" ./sample-workload/deployment_write_print_file.yaml
sed -i "s/{longhaul_agentpool}/$PoolName/g;s/{lustre_fs_name}/$LustreFSName/g;s/{lustre_fs_ip}/$LustreFSIP/g" ./cleanup/cleanupjob.yaml

print_logs_info "Connecting to AKS Cluster=$ClusterName, ResourceGroup=$ResourceGroup, AKS pool=$PoolName"
az configure --defaults group=$ResourceGroup
az aks get-credentials --resource-group $ResourceGroup --name $ClusterName

if [[ -z "${SKIP_FAULT_TEST:-}" ]]; then
	print_logs_case "Executing fault test"
	./fault-test.sh
else
	print_logs_case "Skipping fault test (SKIP_FAULT_TEST is set)"
fi

if [[ -z "${SKIP_UPDATE_TEST:-}" ]]; then
	print_logs_case "Executing update test"
	./update-test.sh
else
	print_logs_case "Skipping update test (SKIP_UPDATE_TEST is set)"
fi

if [[ -z "${SKIP_PERF_SCALE_TEST:-}" ]]; then
	print_logs_case "Executing perf/scale test"
	./perf-scale-test.sh
else
	print_logs_case "Skipping perf/scale test (SKIP_PERF_SCALE_TEST is set)"
fi

if [[ -z "${SKIP_EXTERNAL_E2E_TEST:-}" ]]; then
	print_logs_case "Executing external e2e test"
	./external-e2e.sh
else
	print_logs_case "Skipping external e2e test (SKIP_EXTERNAL_E2E_TEST is set)"
fi

print_logs_case "Executing cleanup"
sleep 180
kubectl apply -f ./cleanup/cleanupjob.yaml
kubectl wait --for=condition=complete job/cleanup
kubectl delete -f ./cleanup/cleanupjob.yaml

print_logs_case "Test suites executed successfully"
