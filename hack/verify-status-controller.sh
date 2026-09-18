#!/usr/bin/env bash

# Copyright 2026 The Kubernetes Authors.
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

CHART=charts/latest/azurelustre-csi-driver
FIXTURES="_output/status-controller-checks-$$"
mkdir -p "${FIXTURES}"
trap 'rm -rf "${FIXTURES}"' EXIT

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

assert_yaml() {
  local expected=$1 query=$2 file=$3 actual
  actual=$(yq eval "${query}" "${file}")
  [[ "${actual}" == "${expected}" ]] || fail "${query}: expected '${expected}', got '${actual}'"
}

rendered="${FIXTURES}/rendered.yaml"
helm template azurelustre "${CHART}" --namespace kube-system >"${rendered}"
election_ns=$(yq 'select(.kind == "Namespace") | .metadata.name' "${rendered}")
[[ "${election_ns}" == azurelustre-control-f78b1d92acb5c8bd47911576df92b345 ]] || fail "unstable election namespace"
assert_yaml "" 'select(.kind == "Namespace") | .metadata.annotations."helm.sh/resource-policy" // ""' "${rendered}"

yq 'select(.kind == "Deployment" and .metadata.labels."app.kubernetes.io/component" == "status-controller")' \
  "${rendered}" >"${FIXTURES}/deployment.yaml"
deployment="${FIXTURES}/deployment.yaml"
assert_yaml 2 '.spec.replicas' "${deployment}"
assert_yaml "${election_ns}" '.spec.template.spec.containers[0].env[] | select(.name == "STATUS_CONTROLLER_ELECTION_NAMESPACE") | .value' "${deployment}"
assert_yaml '/healthz' '.spec.template.spec.containers[0].livenessProbe.httpGet.path' "${deployment}"
assert_yaml '/readyz' '.spec.template.spec.containers[0].readinessProbe.httpGet.path' "${deployment}"
assert_yaml health '.spec.template.spec.containers[0].livenessProbe.httpGet.port' "${deployment}"
assert_yaml health '.spec.template.spec.containers[0].readinessProbe.httpGet.port' "${deployment}"
assert_yaml 29654 '.spec.template.spec.containers[0].ports[] | select(.name == "health") | .containerPort' "${deployment}"
assert_yaml 2 '.spec.template.spec.containers[0].readinessProbe.timeoutSeconds' "${deployment}"
assert_yaml 2 '.spec.template.spec.containers[0].livenessProbe.timeoutSeconds' "${deployment}"
assert_yaml 1 '.spec.template.spec.containers[0].readinessProbe.failureThreshold' "${deployment}"

yq 'select(.kind == "Role" and .metadata.name == "status-controller-election")' "${rendered}" >"${FIXTURES}/election-role.yaml"
assert_yaml "${election_ns}" '.metadata.namespace' "${FIXTURES}/election-role.yaml"
assert_yaml create '.rules[0].verbs | join(",")' "${FIXTURES}/election-role.yaml"
assert_yaml get,update '.rules[1].verbs | join(",")' "${FIXTURES}/election-role.yaml"
assert_yaml azurelustre-node-status-controller '.rules[1].resourceNames | join(",")' "${FIXTURES}/election-role.yaml"
assert_yaml 'leases' '.rules[0].resources | join(",")' "${FIXTURES}/election-role.yaml"
assert_yaml 'leases' '.rules[1].resources | join(",")' "${FIXTURES}/election-role.yaml"
assert_yaml 'get,list,watch' 'select(.kind == "Role" and .metadata.labels."app.kubernetes.io/component" == "status-controller") |
  .rules[] | select(.resources[] == "leases") | .verbs | join(",")' "${rendered}"
yq 'select(.kind == "RoleBinding" and .metadata.name == "status-controller-election")' \
  "${rendered}" >"${FIXTURES}/binding.yaml"
assert_yaml "${election_ns}" '.metadata.namespace' "${FIXTURES}/binding.yaml"
assert_yaml 1 '.subjects | length' "${FIXTURES}/binding.yaml"
assert_yaml csi-azurelustre-status-controller-sa '.subjects[0].name' "${FIXTURES}/binding.yaml"
assert_yaml kube-system '.subjects[0].namespace' "${FIXTURES}/binding.yaml"
node_bindings=$(yq ea '[select(.kind == "RoleBinding" and .subjects[].name == "csi-azurelustre-node-sa") |
  .metadata.namespace] | unique | .[]' "${rendered}")
[[ "${node_bindings}" == kube-system ]] || fail "node Lease bindings escaped the driver namespace"
assert_yaml azurelustre-azurelustre-csi-driver-node-role 'select(.kind == "ClusterRole" and .metadata.name == "azurelustre-azurelustre-csi-driver-node-role") |
  .metadata.name' "${rendered}"
assert_yaml "" 'select(.kind == "ClusterRole" and .metadata.name == "azurelustre-azurelustre-csi-driver-node-role") |
  .rules[] | select(.resources[] == "leases")' "${rendered}"

# Full-length namespaces with identical prefixes must not collapse after
# truncation. Release names are also part of the namespace ownership identity.
long_prefix=$(printf '%062d' 0 | tr '0' 'a')
names=("${election_ns}")
for identity in "${long_prefix}a/azurelustre" "${long_prefix}b/azurelustre" "kube-system/other"; do
  namespace=${identity%/*}
  release=${identity#*/}
  helm template "${release}" "${CHART}" --namespace "${namespace}" \
    --show-only templates/status-controller-namespace.yaml >"${FIXTURES}/namespace.yaml"
  name=$(yq '.metadata.name' "${FIXTURES}/namespace.yaml")
  [[ ${#name} -le 63 && "${name}" =~ ^azurelustre-control-[a-f0-9]{32}$ ]] || fail "invalid election namespace ${name}"
  for previous in "${names[@]}"; do
    [[ "${name}" != "${previous}" ]] || fail "namespace collision for ${identity}"
  done
  names+=("${name}")
done

# Exercise the same ownership helper used around the live lookup without a
# cluster. This does not simulate Helm's API-backed ownership validation.
mkdir -p "${FIXTURES}/ownership/templates"
cp "${CHART}/Chart.yaml" "${FIXTURES}/ownership/"
cp "${CHART}/templates/_helpers.tpl" "${FIXTURES}/ownership/templates/"
cat >"${FIXTURES}/ownership/templates/check.yaml" <<'TEMPLATE'
{{- include "azurelustre.validateElectionNamespaceOwner" (dict "existing" .Values.existing "release" .Release) -}}
TEMPLATE
helm template azurelustre "${FIXTURES}/ownership" --namespace kube-system >/dev/null
owner='{"metadata":{"annotations":{"meta.helm.sh/release-name":"azurelustre","meta.helm.sh/release-namespace":"kube-system"},"labels":{"app.kubernetes.io/managed-by":"Helm","app.kubernetes.io/component":"status-controller-election"}}}'
helm template azurelustre "${FIXTURES}/ownership" --namespace kube-system --set-json "existing=${owner}" >/dev/null
for conflict in '{"metadata":{}}' \
  "${owner/azurelustre/other}" \
  "${owner/kube-system/other}" \
  "${owner/status-controller-election/shared}" \
  "${owner/Helm/other}"; do
  if helm template azurelustre "${FIXTURES}/ownership" --namespace kube-system \
    --set-json "existing=${conflict}" >"${FIXTURES}/conflict.log" 2>&1; then
    fail "adopted conflicting election namespace: ${conflict}"
  fi
  grep -q 'refusing adoption' "${FIXTURES}/conflict.log" || fail "unexpected ownership validation failure"
done
helm template azurelustre "${CHART}" --namespace kube-system --set rbac.create=false >"${rendered}"
assert_yaml "${election_ns}" 'select(.kind == "Namespace") | .metadata.name' "${rendered}"
assert_yaml "" 'select(.kind == "RoleBinding" and .metadata.name == "status-controller-election")' "${rendered}"

# Run static lifecycle scripts with an exported shell mock: never contact a
# cluster. Assert ownership failures happen before any mutation.
export FIXTURES election_ns
export owner_result="" existing_layout="" create_failure=false namespace_get_failure=false
kubectl() {
  printf '%s\n' "$*" >>"${FIXTURES}/kubectl.log"
  case "$*" in
    "create --dry-run=client"*) echo "${election_ns}" ;;
    "get namespace "* | "get -f "*namespace-csi-azurelustre-status-controller.yaml*)
      [[ "${namespace_get_failure}" == false ]] || return 1
      printf '%s' "${owner_result}" ;;
    "create -f "*namespace-csi-azurelustre-status-controller.yaml*)
      [[ "${create_failure}" == false ]] ;;
    "get deployment csi-azurelustre-status-controller"*) printf '%s' "${existing_layout}" ;;
    "get configmap "*) return 1 ;;
    "get daemonset "*) echo '1 1 0 0' ;;
    "get daemonsets.apps "*) return 0 ;;
    "apply "* | "delete "* | "rollout "*) return 0 ;;
    *) echo "Unexpected mock kubectl call: $*" >&2; return 1 ;;
  esac
}
export -f kubectl
for mode in fresh existing migration; do
  : >"${FIXTURES}/kubectl.log"
  owner_result=""
  existing_layout=""
  if [[ "${mode}" != fresh ]]; then
    owner_result="azurelustre-static|status-controller-election|"
    existing_layout="csi-azurelustre-status-controller|${election_ns}"
  fi
  [[ "${mode}" != migration ]] || existing_layout="csi-azurelustre-status-controller|"
  bash deploy/install-driver.sh local >"${FIXTURES}/install.log" 2>&1 || fail "static ${mode} install failed"
  if [[ "${mode}" == migration ]]; then
    grep -q '^delete deployment csi-azurelustre-status-controller .*--cascade=foreground --wait=true' \
      "${FIXTURES}/kubectl.log" || fail "lock migration did not wait for old controllers"
  elif grep -q '^delete deployment csi-azurelustre-status-controller' "${FIXTURES}/kubectl.log"; then
    fail "unchanged lock layout unnecessarily stopped controllers"
  fi
  grep -q '^rollout status deployment csi-azurelustre-status-controller' "${FIXTURES}/kubectl.log" ||
    fail "static installer did not wait for status-controller readiness"
done
for mode in foreign_owner helm_owner read_error create_race; do
  : >"${FIXTURES}/kubectl.log"
  owner_result="other|status-controller-election|"
  namespace_get_failure=false
  create_failure=false
  [[ "${mode}" != helm_owner ]] || owner_result="Helm|status-controller-election|azurelustre"
  [[ "${mode}" != read_error ]] || namespace_get_failure=true
  if [[ "${mode}" == create_race ]]; then
    owner_result=""
    create_failure=true
  fi
  if bash deploy/install-driver.sh local >"${FIXTURES}/install.log" 2>&1; then
    fail "static installer accepted ${mode}"
  fi
  if grep -Eq '^(apply|delete) ' "${FIXTURES}/kubectl.log"; then
    fail "static installer mutated resources after ${mode}"
  fi
done
namespace_get_failure=false
create_failure=false
owner_result="azurelustre-static|status-controller-election|"
: >"${FIXTURES}/kubectl.log"
bash deploy/uninstall-driver.sh >"${FIXTURES}/uninstall.log" 2>&1 || fail "static uninstall failed"
deployment_line=$(grep -n '^delete -f .*deploy/csi-azurelustre-status-controller.yaml .*--cascade=foreground --wait=true' "${FIXTURES}/kubectl.log" | cut -d: -f1)
namespace_line=$(grep -n '^delete -f .*deploy/namespace-csi-azurelustre-status-controller.yaml' "${FIXTURES}/kubectl.log" | cut -d: -f1)
[[ -n "${deployment_line}" && -n "${namespace_line}" && "${deployment_line}" -lt "${namespace_line}" ]] ||
  fail "static uninstall did not wait for controllers before deleting their namespace"
owner_result="Helm|status-controller-election|azurelustre"
: >"${FIXTURES}/kubectl.log"
if bash deploy/uninstall-driver.sh >"${FIXTURES}/uninstall.log" 2>&1; then
  fail "static uninstall accepted Helm ownership"
fi
if grep -q '^delete ' "${FIXTURES}/kubectl.log"; then
  fail "static uninstall mutated resources despite conflicting namespace ownership"
fi
echo "Status-controller probes, election isolation, stable naming and ownership guards verified."
