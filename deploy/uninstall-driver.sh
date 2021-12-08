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

ver="main"
if [[ "$#" -gt 0 ]]; then
  ver="$1"
fi

repo="https://raw.githubusercontent.com/kubernetes-sigs/amlfs-csi-driver/$ver/deploy"
if [[ "$#" -gt 1 ]]; then
  if [[ "$2" == *"local"* ]]; then
    echo "use local deploy"
    repo="./deploy"
  fi
fi

if [ $ver != "master" ]; then
  repo="$repo/$ver"
fi

echo "Uninstalling Azure Managed Lustre CSI driver, version: $ver ..."
kubectl delete -f $repo/csi-amlfs-controller.yaml --ignore-not-found
kubectl delete -f $repo/csi-amlfs-node.yaml --ignore-not-found
kubectl delete -f $repo/csi-amlfs-driver.yaml --ignore-not-found
kubectl delete -f $repo/rbac-csi-amlfs-controller.yaml --ignore-not-found
kubectl delete -f $repo/rbac-csi-amlfs-node.yaml --ignore-not-found
echo 'Uninstalled Azure Managed Lustre CSI driver successfully.'
