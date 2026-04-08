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

# Ensure required variables are set
: "${IMAGE_NAME:?Required variable IMAGE_NAME is not set}" # Ex: upstream.azurecr.io/oss/v2/kubernetes-csi/azurelustre-csi:latest-jammy

echo "IMAGE_NAME: ${IMAGE_NAME}"

# Create a configmap for the integration test script
kubectl delete configmap integration-dalec-script --ignore-not-found
kubectl create configmap integration-dalec-script --from-file=run_integration_test.sh

# Show the filled in template
envsubst < integration_dalec_aks.yaml.template

# Make sure to delete any previous instances of the pod
envsubst < integration_dalec_aks.yaml.template | kubectl delete -f - --ignore-not-found
envsubst < integration_dalec_aks.yaml.template | kubectl apply -f -

# Now running - wait for completion!
pod=azurelustre-integration-dalec
kubectl wait --for=condition=Ready "pod/${pod}" --timeout=300s
kubectl wait --for=condition=Ready=false "pod/${pod}" --timeout=300s
# Grab Result. Filter by container name so a future sidecar injection (logging,
# service mesh, etc.) doesn't make the jsonpath return multiple exit codes and
# break `exit "${result}"` with "numeric argument required".
result=$(kubectl get pod "${pod}" -o=jsonpath="{.status.containerStatuses[?(@.name==\"${pod}\")].state.*.exitCode}")
kubectl logs "${pod}"
echo "Result: ${result}"
exit "${result}"
