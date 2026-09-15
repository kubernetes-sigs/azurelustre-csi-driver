#!/usr/bin/env bash

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

for i in $(kubectl get daemonsets.apps -n kube-system -l app=csi-azurelustre-node -o name); do
  kubectl delete -n kube-system "${i}"
done

echo "Uninstalling Azure Lustre CSI driver, repo: ${repo} ..."
kubectl delete -f "${repo}"/csi-azurelustre-controller.yaml --ignore-not-found
kubectl delete -f "${repo}"/pdb-csi-azurelustre-controller.yaml --ignore-not-found
kubectl delete -f "${repo}"/csi-azurelustre-node-jammy.yaml --ignore-not-found
kubectl delete -f "${repo}"/csi-azurelustre-node-noble.yaml --ignore-not-found
kubectl delete -f "${repo}"/csi-azurelustre-node-azurelinux3.yaml --ignore-not-found
kubectl delete -f "${repo}"/csi-azurelustre-driver.yaml --ignore-not-found
kubectl delete -f "${repo}"/rbac-csi-azurelustre-controller.yaml --ignore-not-found
kubectl delete -f "${repo}"/rbac-csi-azurelustre-node.yaml --ignore-not-found
kubectl delete configmap csi-azurelustre-entrypoint -n kube-system --ignore-not-found

# Clean up legacy RBAC resources from older installs.
# Controller provisioner role/binding were renamed (added csi- prefix) after v0.4.0.
kubectl delete clusterrole azurelustre-external-provisioner-role --ignore-not-found
kubectl delete clusterrolebinding azurelustre-csi-provisioner-binding --ignore-not-found
# Secret RBAC resources were removed after v0.4.0.
kubectl delete clusterrole csi-azurelustre-controller-secret-role --ignore-not-found
kubectl delete clusterrolebinding csi-azurelustre-controller-secret-binding --ignore-not-found
kubectl delete clusterrole csi-azurelustre-node-secret-role --ignore-not-found
kubectl delete clusterrolebinding csi-azurelustre-node-secret-binding --ignore-not-found

echo 'Uninstalled Azure Lustre CSI driver successfully.'
