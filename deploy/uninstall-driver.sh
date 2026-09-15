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

# Namespace the driver was installed into. Must match the value used at install
# time (defaults to kube-system). Override via NAMESPACE=<ns>.
NAMESPACE="${NAMESPACE:-kube-system}"

# Delete a manifest, rewriting its `namespace: kube-system` references to the
# configured NAMESPACE so namespaced objects are removed from the right place.
function delete_manifest {
  local src="$1"
  sed "s/namespace: kube-system/namespace: ${NAMESPACE}/g" "${src}" \
    | kubectl delete -f - --ignore-not-found
}

# The manifests include cluster-scoped, cluster-global singletons (the CSIDriver
# object and ClusterRole/ClusterRoleBinding) that a live install in ANOTHER
# namespace depends on. If the driver isn't in ${NAMESPACE} but is installed
# elsewhere, refuse — otherwise we would decapitate that install while leaving
# its pods running. Set FORCE=1 to override (e.g. cleaning up leftover RBAC).
if ! kubectl get deploy -n "${NAMESPACE}" -l app=csi-azurelustre-controller \
    -o name 2>/dev/null | grep -q .; then
  other_ns="$(kubectl get deploy -A -l app=csi-azurelustre-controller \
    -o jsonpath='{range .items[*]}{.metadata.namespace}{"\n"}{end}' 2>/dev/null \
    | sort -u | grep -vx "${NAMESPACE}" || true)"
  if [[ -n "${other_ns}" && "${FORCE:-0}" != "1" ]]; then
    echo "Error: no csi-azurelustre-controller in namespace '${NAMESPACE}', but it" >&2
    echo "appears installed in: ${other_ns//$'\n'/ }" >&2
    echo "Re-run with NAMESPACE set to that namespace. Refusing to delete the shared" >&2
    echo "cluster-scoped CSIDriver/ClusterRoles that install depends on (set FORCE=1 to override)." >&2
    exit 1
  fi
fi

for i in $(kubectl get daemonsets.apps -n "${NAMESPACE}" -l app=csi-azurelustre-node -o name); do
  kubectl delete -n "${NAMESPACE}" "${i}"
done

echo "Uninstalling Azure Lustre CSI driver, repo: ${repo}, namespace: ${NAMESPACE} ..."
delete_manifest "${repo}/csi-azurelustre-controller.yaml"
delete_manifest "${repo}/pdb-csi-azurelustre-controller.yaml"
delete_manifest "${repo}/csi-azurelustre-node-jammy.yaml"
delete_manifest "${repo}/csi-azurelustre-node-noble.yaml"
delete_manifest "${repo}/csi-azurelustre-node-azurelinux3.yaml"
delete_manifest "${repo}/csi-azurelustre-driver.yaml"
delete_manifest "${repo}/rbac-csi-azurelustre-controller.yaml"
delete_manifest "${repo}/rbac-csi-azurelustre-node.yaml"
kubectl delete configmap csi-azurelustre-entrypoint -n "${NAMESPACE}" --ignore-not-found

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
