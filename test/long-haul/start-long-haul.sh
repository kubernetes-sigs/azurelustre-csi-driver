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

if [[ -z "${SKIP_CLEANUP:-}" ]]; then
	print_logs_case "Executing cleanup"

	# Clean up resources left behind by test suites.
	# The sample workload (from fault-test / update-test) may still be running.
	# Wrap pre-cleanup steps with `|| true` so the cleanup job below always runs;
	# it's the last-resort full sweep and must not be skipped on a partial failure.
	print_logs_info "Stopping sample workload if still running"
	stop_sample_workload || true

	# Clean up external-e2e StorageClass if it still exists.
	# (External-e2e PVC is deleted by external-e2e/run.sh's EXIT trap.)
	print_logs_info "Cleaning up external e2e test resources"
	kubectl delete sc testazurelustre.csi.azure.com --ignore-not-found --wait=true || true

	# Wait for any test PVs to be cleaned up, scoped to the StorageClasses
	# this suite is known to provision against (sample workload, cleanup job,
	# external-e2e). Scoping prevents this cleanup from waiting on unrelated
	# Azure Lustre PVs in shared clusters.
	# `|| true` on each substitution guards against transient apiserver errors
	# aborting cleanup under errexit.
	print_logs_info "Waiting for test PVs to be deleted"
	test_scs="azurelustre-longhaulsample-sc azurelustre-longhaulcleanup-sc testazurelustre.csi.azure.com"
	for sc in ${test_scs}; do
		for pv in $(kubectl get pv -o jsonpath="{.items[?(@.spec.storageClassName==\"${sc}\")].metadata.name}" || true); do
			kubectl wait --for=delete "pv/${pv}" --timeout=300s || true
		done
	done

	kubectl apply -f ./cleanup/cleanupjob.yaml
	# Capture the wait status so the worker Job/PVC gets deleted even when
	# the cleanup job times out, then re-report the failure so we don't
	# silently swallow a cleanup that never completed.
	cleanup_status=0
	kubectl wait --for=condition=complete job/cleanup --timeout=600s || cleanup_status=$?
	kubectl delete -f ./cleanup/cleanupjob.yaml
	if [[ "${cleanup_status}" -ne 0 ]]; then
		print_logs_error "cleanup job did not complete within 600s"
		exit 1
	fi
else
	print_logs_case "Skipping cleanup (SKIP_CLEANUP is set)"
fi

print_logs_case "Test suites executed successfully"
