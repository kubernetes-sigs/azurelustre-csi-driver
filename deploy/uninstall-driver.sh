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

readonly driver_name="azurelustre.csi.azure.com"
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly repo

usage() {
  echo "Usage: $0 [--force]"
}

force=false
while (($# > 0)); do
  case "$1" in
    --force)
      force=true
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      echo "Unknown argument: $1" >&2
      exit 2
      ;;
  esac
  shift
done

if [[ "${force}" == "true" ]]; then
  echo "WARNING: --force skips the PersistentVolume safety check." >&2
else
  readonly pv_jsonpath="{range .items[?(@.spec.csi.driver==\"${driver_name}\")]}{.metadata.name}{\"\\n\"}{end}"
  if ! persistent_volumes=$(kubectl get persistentvolumes -o="jsonpath=${pv_jsonpath}"); then
    echo "ERROR: could not list PersistentVolumes; blocking uninstall." >&2
    echo "Resolve Kubernetes API access or rerun with --force to bypass the safety check." >&2
    exit 1
  fi

  if [[ -n "${persistent_volumes}" ]]; then
    echo "ERROR: PersistentVolumes still use CSI driver ${driver_name}:" >&2
    printf '%s\n' "${persistent_volumes}" >&2
    echo "Delete these volumes before uninstalling, or rerun with --force to bypass the safety check." >&2
    exit 1
  fi
fi

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
# namespace depends on. Always check all namespaces, even if the target namespace
# has a controller. Include node-only/partial installs and legacy labels, and let
# discovery errors abort before deleting anything. FORCE=1 overrides conflicts,
# not failed discovery.
existing_ns=""
for selector in \
  "app.kubernetes.io/name=azurelustre-csi-driver" \
  "app.kubernetes.io/part-of=azurelustre-csi-driver" \
  "app in (csi-azurelustre-controller,csi-azurelustre-node)"; do
  namespaces="$(kubectl get deployments.apps,daemonsets.apps,pods --all-namespaces \
    -l "${selector}" -o jsonpath='{range .items[*]}{.metadata.namespace}{"\n"}{end}')"
  existing_ns+="${namespaces}"$'\n'
done
other_ns="$(printf '%s' "${existing_ns}" | sort -u | sed '/^$/d' \
  | grep -Fvx -- "${NAMESPACE}" || true)"
if [[ -n "${other_ns}" && "${FORCE:-0}" != "1" ]]; then
  echo "Error: Azure Lustre CSI driver workloads exist in other namespace(s): ${other_ns//$'\n'/ }" >&2
  echo "Re-run with NAMESPACE set to the install namespace. Refusing to delete shared" >&2
  echo "cluster-scoped CSIDriver/ClusterRoles while workloads remain elsewhere (set FORCE=1 to override)." >&2
  exit 1
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
