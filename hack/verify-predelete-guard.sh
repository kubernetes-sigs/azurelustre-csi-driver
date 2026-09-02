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

# Behavioral tests for Helm and direct-install pre-delete guards. No Kubernetes
# cluster is required.

set -euo pipefail

PKG_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
CHART_DIR="${PKG_ROOT}/charts/latest/azurelustre-csi-driver"
UNINSTALL_SCRIPT="${PKG_ROOT}/deploy/uninstall-driver.sh"

WORK_DIR=$(mktemp -d)
trap 'rm -rf "${WORK_DIR}"' EXIT

if ! command -v helm >/dev/null 2>&1; then
  echo "Cannot find helm. Please install helm first." >&2
  exit 1
fi

# shellcheck source=hack/ensure-yq.sh
source "${PKG_ROOT}/hack/ensure-yq.sh"
ensure_yq "${WORK_DIR}"

STUB_DIR="${WORK_DIR}/stubs"
mkdir -p "${STUB_DIR}"
cat >"${STUB_DIR}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"${KUBECTL_LOG}"
if [[ "${1:-} ${2:-}" == "get persistentvolumes" ]]; then
  expected_output='-o=jsonpath={range .items[?(@.spec.csi.driver=="azurelustre.csi.azure.com")]}{.metadata.name}{"\n"}{end}'
  if [[ "${3:-}" != "${expected_output}" ]]; then
    echo "unexpected PersistentVolume output expression: ${3:-<missing>}" >&2
    exit 2
  fi
  if [[ "${KUBECTL_GET_EXIT:-0}" -ne 0 ]]; then
    echo "simulated PersistentVolume list failure" >&2
    exit "${KUBECTL_GET_EXIT}"
  fi
  printf '%s' "${KUBECTL_PV_OUTPUT:-}"
fi
EOF
chmod +x "${STUB_DIR}/kubectl"

FAILURES=0
KUBECTL_LOG="${WORK_DIR}/kubectl.log"

run_uninstall_case() {
  local name=$1 pv_output=$2 get_exit=$3 force=$4 expect_block=$5 expect_delete=$6 expected_text=$7
  local -a args=()
  [[ "${force}" == "1" ]] && args+=(--force)
  : >"${KUBECTL_LOG}"

  local output rc
  set +e
  output=$(PATH="${STUB_DIR}:${PATH}" KUBECTL_LOG="${KUBECTL_LOG}" \
    KUBECTL_PV_OUTPUT="${pv_output}" KUBECTL_GET_EXIT="${get_exit}" \
    bash "${UNINSTALL_SCRIPT}" "${args[@]}" 2>&1)
  rc=$?
  set -e

  local blocked=0 deleted=0
  [[ "${rc}" -ne 0 ]] && blocked=1
  grep -q '^delete ' "${KUBECTL_LOG}" && deleted=1

  if [[ "${force}" == "1" ]] && grep -q '^get persistentvolumes ' "${KUBECTL_LOG}"; then
    echo "FAIL: ${name}: --force invoked the PersistentVolume check"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ "${blocked}" == "${expect_block}" && "${deleted}" == "${expect_delete}" ]] && \
    grep -qF -- "${expected_text}" <<<"${output}"; then
    echo "PASS: ${name} (blocked=${blocked}, deleted=${deleted})"
  else
    echo "FAIL: ${name}"
    echo "  expected blocked=${expect_block}, deleted=${expect_delete}, text=${expected_text}"
    echo "  got exit=${rc}, blocked=${blocked}, deleted=${deleted}"
    echo "  output: ${output}"
    echo "  kubectl calls:"
    sed 's/^/    /' "${KUBECTL_LOG}"
    FAILURES=$((FAILURES + 1))
  fi
}

echo "== Testing rendered Helm guard =="
rendered=$(helm template chart-test "${CHART_DIR}" --namespace kube-system \
  --set "fullnameOverride=$(printf 'a%.0s' {1..63})")
job_name=$(yq eval 'select(.kind == "Job") | .metadata.name' - <<<"${rendered}")
job_command=$(yq eval 'select(.kind == "Job") | .spec.template.spec.containers[0].command[0]' - <<<"${rendered}")
job_args=$(yq eval -o=json -I=0 'select(.kind == "Job") | .spec.template.spec.containers[0].args' - <<<"${rendered}")
job_ttl=$(yq eval 'select(.kind == "Job") | .spec.ttlSecondsAfterFinished' - <<<"${rendered}")
job_image=$(yq eval 'select(.kind == "Job") | .spec.template.spec.containers[0].image' - <<<"${rendered}")
job_pull_policy=$(yq eval 'select(.kind == "Job") | .spec.template.spec.containers[0].imagePullPolicy' - <<<"${rendered}")
job_hook=$(yq eval 'select(.kind == "Job") | .metadata.annotations."helm.sh/hook"' - <<<"${rendered}")
job_delete_policy=$(yq eval 'select(.kind == "Job") | .metadata.annotations."helm.sh/hook-delete-policy"' - <<<"${rendered}")
rbac_verbs=$(yq eval -o=json -I=0 'select(.kind == "ClusterRole" and (.metadata.name | test("predelete-guard$"))) | .rules[0].verbs' - <<<"${rendered}")
driver_repository=$(yq eval '.image.repository' "${CHART_DIR}/values.yaml")
driver_tag=$(yq eval '.image.tag' "${CHART_DIR}/values.yaml")

if [[ ${#job_name} -le 63 && "${job_name}" == *-predelete-guard && \
  "${job_command}" == "/app/azurelustreplugin" && \
  "${job_args}" == *'--pre-delete-check'* && "${job_args}" == *'--pre-delete-check-timeout=30s'* && \
  "${job_image}" == "${driver_repository}:${driver_tag}-noble" && \
  "${job_pull_policy}" == "Always" && "${job_hook}" == "pre-delete" && \
  "${job_delete_policy}" == "before-hook-creation,hook-succeeded" && \
  "${job_ttl}" == "300" && "${rbac_verbs}" == '["list"]' ]]; then
  echo "PASS: rendered Helm guard contract"
else
  echo "FAIL: rendered Helm guard contract"
  printf '  name=%s\n  command=%s\n  args=%s\n  image=%s\n  pullPolicy=%s\n  hook=%s\n  deletePolicy=%s\n  ttl=%s\n  verbs=%s\n' \
    "${job_name}" "${job_command}" "${job_args}" "${job_image}" "${job_pull_policy}" \
    "${job_hook}" "${job_delete_policy}" "${job_ttl}" "${rbac_verbs}"
  FAILURES=$((FAILURES + 1))
fi

rendered=$(helm template chart-test "${CHART_DIR}" -s templates/predelete-guard-job.yaml \
  --set-string image.repository=example.invalid/driver,image.tag=guard-candidate \
  --set image.pullPolicy=Never \
  --set-string csidriver.name=test.csi.example.com,preDeleteGuard.checkTimeout=45s)
job_image=$(yq eval '.spec.template.spec.containers[0].image' - <<<"${rendered}")
job_pull_policy=$(yq eval '.spec.template.spec.containers[0].imagePullPolicy' - <<<"${rendered}")
job_args=$(yq eval -o=json -I=0 '.spec.template.spec.containers[0].args' - <<<"${rendered}")
if [[ "${job_image}" == "example.invalid/driver:guard-candidate-noble" && \
  "${job_pull_policy}" == "Never" && \
  "${job_args}" == '["--pre-delete-check","--pre-delete-check-timeout=45s","--drivername=test.csi.example.com"]' ]]; then
  echo "PASS: guard follows the selected driver image, pull policy, name, and timeout"
else
  echo "FAIL: guard image/argument overrides"
  printf '  image=%s\n  pullPolicy=%s\n  args=%s\n' "${job_image}" "${job_pull_policy}" "${job_args}"
  FAILURES=$((FAILURES + 1))
fi

rendered=$(helm template chart-test "${CHART_DIR}" -s templates/predelete-guard-job.yaml \
  --set image.pullPolicy=Never,preDeleteGuard.imagePullPolicy=IfNotPresent)
job_pull_policy=$(yq eval '.spec.template.spec.containers[0].imagePullPolicy' - <<<"${rendered}")
if [[ "${job_pull_policy}" == "IfNotPresent" ]]; then
  echo "PASS: explicit guard pull policy overrides the driver policy"
else
  echo "FAIL: expected explicit IfNotPresent override, got ${job_pull_policy}"
  FAILURES=$((FAILURES + 1))
fi

echo "== Testing direct uninstall guard =="
run_uninstall_case "allow: no matching PVs" "" 0 0 0 1 "Uninstalled Azure Lustre CSI driver successfully."
run_uninstall_case "block: matching PVs" $'pv-a\npv-b\n' 0 0 1 0 "pv-a"
run_uninstall_case "block: API failure" "" 1 0 1 0 "could not list PersistentVolumes"
run_uninstall_case "allow: explicit force" "" 1 1 0 1 "--force skips the PersistentVolume safety check"

echo
if [[ "${FAILURES}" -ne 0 ]]; then
  echo "Pre-delete guard verification FAILED (${FAILURES} case(s))."
  exit 1
fi
echo "Pre-delete guard verification succeeded!"
