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

set -euo pipefail

repo="$(git rev-parse --show-toplevel)/deploy"
ver="main"
workload="azurelustre"

if [[ "$#" -gt 0 ]]; then
  ver="$1"
fi

if [[ "$#" -gt 1 ]] && [[ "$2" == *"remote"* ]]; then
  repo="https://raw.githubusercontent.com/kubernetes-sigs/azurelustre-csi-driver/$ver/deploy"
fi

if [[ "$#" -gt 2 ]]; then
  workload="$3"
fi

if [ $workload != "noworkload" ]; then
  workload="$repo/example/$workload"
fi

if [ $ver != "main" ]; then
  repo="$repo/$ver"
fi

echo "Installing Azure Lustre CSI driver, repo: $repo ..."
kubectl apply -f $repo/rbac-csi-azurelustre-controller.yaml
kubectl apply -f $repo/rbac-csi-azurelustre-node.yaml
kubectl apply -f $repo/csi-azurelustre-driver.yaml
kubectl apply -f $repo/csi-azurelustre-controller.yaml
kubectl apply -f $repo/csi-azurelustre-node.yaml
echo 'Azure Lustre CSI driver installed successfully.'

if [ $workload != "noworkload" ]; then
  echo "Installing workload, repo: $workload ..."
  kubectl apply -f $workload
  echo 'Workload installed successfully.'
else
  echo 'No workload.'
fi
