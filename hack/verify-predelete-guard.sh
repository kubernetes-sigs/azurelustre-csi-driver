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

# Behavioral test for the Helm pre-delete guard hook.
#
# The guard is a shell script embedded in the pre-delete Job template. It lists
# PersistentVolumes and BLOCKS `helm uninstall` (non-zero exit) while any PV
# still references this driver, so operators cannot orphan billing AMLFS
# filesystems or leave PVs stuck Terminating.
#
# This test renders the ACTUAL shipped script (via `helm template` + `yq`) and
# runs it against canned kubectl output through a PATH stub. It verifies that
# driver-owned PVs block, empty results allow, kubectl errors fail closed, and
# the shipped helper image is MCR-hosted and digest-pinned.
# No Kubernetes cluster is required.

set -euo pipefail

PKG_ROOT=$(git rev-parse --show-toplevel)
CHART_DIR="${PKG_ROOT}/charts/latest/azurelustre-csi-driver"

WORK_DIR=$(mktemp -d)
trap 'rm -rf "${WORK_DIR}"' EXIT

if ! command -v helm >/dev/null 2>&1; then
  echo "Cannot find helm. Please install helm first." >&2
  exit 1
fi

# mikefarah yq is required to pull the script out of the rendered YAML block
# scalar. Install it locally (same approach as verify-helm-chart-files.sh) when
# a suitable yq is not already on PATH.
YQ_VERSION="v4.53.3"
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -qi mikefarah; then
  echo "Cannot find mikefarah yq. Installing ${YQ_VERSION} ..."
  yq_arch=$(uname -m)
  case "${yq_arch}" in
    x86_64) yq_arch=amd64 ;;
    aarch64 | arm64) yq_arch=arm64 ;;
    *)
      echo "Unsupported architecture: ${yq_arch}, must be x86_64 or aarch64" >&2
      exit 1
      ;;
  esac
  curl -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${yq_arch}" -o "${WORK_DIR}/yq"
  chmod +x "${WORK_DIR}/yq"
  export PATH="${WORK_DIR}:${PATH}"
fi

# Extract the guard script (containers[0].command == [/bin/sh, -c, <script>]).
GUARD_SCRIPT="${WORK_DIR}/guard.sh"
RENDERED_JOB="${WORK_DIR}/guard.yaml"
helm template test "${CHART_DIR}" -s templates/predelete-guard-job.yaml >"${RENDERED_JOB}"
yq '.spec.template.spec.containers[0].command[2]' "${RENDERED_JOB}" >"${GUARD_SCRIPT}"

if [[ ! -s "${GUARD_SCRIPT}" ]]; then
  echo "ERROR: failed to render/extract the pre-delete guard script." >&2
  exit 1
fi

GUARD_IMAGE=$(yq '.spec.template.spec.containers[0].image' "${RENDERED_JOB}")
if [[ ! "${GUARD_IMAGE}" =~ ^mcr\.microsoft\.com/.+@sha256:[0-9a-f]{64}$ ]]; then
  echo "ERROR: pre-delete guard image is not an MCR digest reference: ${GUARD_IMAGE}" >&2
  exit 1
fi

printf 'pv-a\npv-b\n' >"${WORK_DIR}/two_pvs.txt"
: >"${WORK_DIR}/empty.txt"

# --- kubectl stub -----------------------------------------------------------
STUB_DIR="${WORK_DIR}/stubs"
mkdir -p "${STUB_DIR}"
cat >"${STUB_DIR}/kubectl" <<'EOF'
#!/bin/sh
if [ "${KUBECTL_EXIT:-0}" -ne 0 ]; then
  echo "kubectl: simulated API failure" >&2
  exit "${KUBECTL_EXIT}"
fi
cat "${FIXTURE}"
EOF
chmod +x "${STUB_DIR}/kubectl"

# --- test harness -----------------------------------------------------------
FAILURES=0

# run_case <name> <fixture> <kubectl_exit> <expect_block:0|1> [expect_substr]
run_case() {
  local name=$1 fixture=$2 kubectl_exit=$3 expect_block=$4 expect_substr=${5:-}

  local out rc
  set +e
  out=$(PATH="${STUB_DIR}:${PATH}" FIXTURE="${WORK_DIR}/${fixture}" KUBECTL_EXIT="${kubectl_exit}" \
    /bin/sh "${GUARD_SCRIPT}" 2>&1)
  rc=$?
  set -e

  local blocked=0
  [[ "${rc}" -ne 0 ]] && blocked=1

  local ok=1
  [[ "${blocked}" == "${expect_block}" ]] || ok=0
  if [[ -n "${expect_substr}" ]] && ! grep -qF "${expect_substr}" <<<"${out}"; then
    ok=0
  fi

  if [[ "${ok}" == "1" ]]; then
    echo "PASS: ${name} (exit ${rc}, blocked=${blocked})"
  else
    echo "FAIL: ${name}"
    echo "  expected block=${expect_block}${expect_substr:+, substring \"${expect_substr}\"}"
    echo "  got exit=${rc} (blocked=${blocked})"
    echo "  output: ${out}"
    FAILURES=$((FAILURES + 1))
  fi
}

echo "== Testing pre-delete guard detection logic =="
# name                              fixture       kubectl block substr
run_case "block: 2 driver PVs"       two_pvs.txt   0       1 "found 2"
run_case "allow: no driver PVs"      empty.txt     0       0 "allowing uninstall"
run_case "block: kubectl/API failure" empty.txt     1       1 "could not list PersistentVolumes"

echo
if [[ "${FAILURES}" -ne 0 ]]; then
  echo "Pre-delete guard verification FAILED (${FAILURES} case(s))."
  exit 1
fi
echo "Pre-delete guard verification succeeded!"
