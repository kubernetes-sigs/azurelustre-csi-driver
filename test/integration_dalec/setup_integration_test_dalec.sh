#!/usr/bin/env bash

# Copyright 2024 The Kubernetes Authors.
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

set -euox pipefail

# IMAGE_NAME is the driver image under test (the DALEC-built distro variant).
# CSC_IMAGE_NAME is the test-only sidecar carrying the csc client.
: "${IMAGE_NAME:?Required variable IMAGE_NAME is not set}"       # Ex: upstream.azurecr.io/oss/v2/kubernetes-csi/azurelustre-csi:latest-azurelinux3
: "${CSC_IMAGE_NAME:?Required variable CSC_IMAGE_NAME is not set}" # Ex: azurelustre-csi-test/csc:local

echo "IMAGE_NAME: ${IMAGE_NAME}"
echo "CSC_IMAGE_NAME: ${CSC_IMAGE_NAME}"

pod=azurelustre-integration-dalec
readonly pod

# Create a configmap for the integration test script
kubectl delete configmap integration-dalec-script --ignore-not-found
kubectl create configmap integration-dalec-script --from-file=run_integration_test.sh

# Show the filled in template
envsubst < integration_dalec_aks.yaml.template

# Make sure to delete any previous instances of the pod
envsubst < integration_dalec_aks.yaml.template | kubectl delete -f - --ignore-not-found
envsubst < integration_dalec_aks.yaml.template | kubectl apply -f -

# The driver container runs for the life of the pod, so we cannot wait on the
# whole pod terminating. Instead wait for the "tester" container to finish and
# read its exit code. The "driver" and "tester" container names must match the
# pod template.
tester_container=tester
driver_container=driver

# Container waiting reasons that will never resolve on their own (bad/missing
# image, crash loop, misconfig). If either container hits one of these, fail
# fast instead of waiting out the full timeout.
fatal_waiting_reason() {
  local reason
  for c in "${tester_container}" "${driver_container}"; do
    reason=$(kubectl get pod "${pod}" \
      -o=jsonpath="{.status.containerStatuses[?(@.name==\"${c}\")].state.waiting.reason}" 2>/dev/null || echo "")
    case "${reason}" in
      ImagePullBackOff|ErrImagePull|InvalidImageName|CrashLoopBackOff|CreateContainerConfigError|CreateContainerError)
        echo "ERROR: ${c} container is stuck in ${reason}; aborting." >&2
        return 0
        ;;
      *)
        # Empty or transient reason (ContainerCreating, PodInitializing, etc.):
        # not fatal, keep waiting.
        ;;
    esac
  done
  return 1
}

echo "Waiting for the ${tester_container} container to complete..."
result=""
for _ in $(seq 1 300); do
  # Surface pod-level scheduling/image failures early with a clear message.
  phase=$(kubectl get pod "${pod}" -o=jsonpath="{.status.phase}" 2>/dev/null || echo "")
  result=$(kubectl get pod "${pod}" \
    -o=jsonpath="{.status.containerStatuses[?(@.name==\"${tester_container}\")].state.terminated.exitCode}" 2>/dev/null || echo "")
  if [[ -n "${result}" ]]; then
    break
  fi
  if [[ "${phase}" == "Failed" ]]; then
    break
  fi
  # Catch image-pull/scheduling/crash failures that leave the pod Pending or
  # Running (not Failed) so we don't block for the whole timeout window.
  if fatal_waiting_reason; then
    break
  fi
  sleep 2
done

echo "===== ${tester_container} logs ====="
kubectl logs "${pod}" -c "${tester_container}" || true

# Validate result is a non-empty integer before exiting. An empty or non-numeric
# value (container never terminated, name mismatch, jsonpath miss) would cause
# `exit ""` to fail with "numeric argument required" and leave no clear signal.
# Allow negatives because Kubernetes types exitCode as int32 (containerd/CRI-O
# normally report 0-255, but be permissive).
if [[ ! "${result}" =~ ^-?[0-9]+$ ]]; then
  echo "ERROR: integration test container did not produce a valid exit code." >&2
  echo "===== ${driver_container} logs (for debugging) =====" >&2
  kubectl logs "${pod}" -c "${driver_container}" >&2 || true
  echo "===== pod state (for debugging) =====" >&2
  kubectl get pod "${pod}" -o yaml >&2 || true
  exit 1
fi

echo "Result: ${result}"
if [[ "${result}" -ne 0 ]]; then
  echo "===== ${driver_container} logs (test failed) =====" >&2
  kubectl logs "${pod}" -c "${driver_container}" >&2 || true
fi

exit "${result}"
