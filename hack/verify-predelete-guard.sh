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
# runs it against canned apiserver payloads, stubbing `curl` (and, for one case,
# `grep`) through PATH. It pins down the two failure modes that previously
# shipped as fail-OPEN bugs and were only caught in live testing:
#   * pretty-printed JSON (spec.csi.driver as `"driver": "<name>"`, with a
#     space) must still be counted -- a whitespace-blind match miscounted 0.
#   * a scan/tool error (grep absent or failing) must fail CLOSED and block.
# No Kubernetes cluster is required.

set -euo pipefail

PKG_ROOT=$(git rev-parse --show-toplevel)
CHART_DIR="${PKG_ROOT}/charts/latest/azurelustre-csi-driver"
DRIVER="azurelustre.csi.azure.com"

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
helm template test "${CHART_DIR}" -s templates/predelete-guard-job.yaml 2>/dev/null \
  | yq '.spec.template.spec.containers[0].command[2]' >"${GUARD_SCRIPT}"

if [[ ! -s "${GUARD_SCRIPT}" ]]; then
  echo "ERROR: failed to render/extract the pre-delete guard script." >&2
  exit 1
fi

# --- Fixtures: apiserver /persistentvolumes responses -----------------------
# Two driver-owned PVs, COMPACT JSON.
cat >"${WORK_DIR}/two_compact.json" <<EOF
{"kind":"PersistentVolumeList","items":[{"metadata":{"name":"pv-a"},"spec":{"csi":{"driver":"${DRIVER}"}}},{"metadata":{"name":"pv-b"},"spec":{"csi":{"driver":"${DRIVER}"}}}]}
EOF

# Two driver-owned PVs, PRETTY-PRINTED JSON (the regression case: note the
# space after each colon).
cat >"${WORK_DIR}/two_pretty.json" <<EOF
{
  "kind": "PersistentVolumeList",
  "items": [
    { "metadata": { "name": "pv-a" }, "spec": { "csi": { "driver": "${DRIVER}" } } },
    { "metadata": { "name": "pv-b" }, "spec": { "csi": { "driver": "${DRIVER}" } } }
  ]
}
EOF

# No PersistentVolumes at all.
cat >"${WORK_DIR}/empty.json" <<EOF
{"kind":"PersistentVolumeList","items":[]}
EOF

# Only a PV from a DIFFERENT CSI driver -> must not be counted.
cat >"${WORK_DIR}/other_driver.json" <<EOF
{"kind":"PersistentVolumeList","items":[{"metadata":{"name":"pv-x"},"spec":{"csi":{"driver":"disk.csi.azure.com"}}}]}
EOF

# --- curl / grep stubs ------------------------------------------------------
STUB_DIR="${WORK_DIR}/stubs"
mkdir -p "${STUB_DIR}"
# curl stub: ignore all args; emit ${FIXTURE}; exit ${CURL_EXIT:-0}. Emulates a
# fetch failure when CURL_EXIT is non-zero (curl --fail on an HTTP error).
cat >"${STUB_DIR}/curl" <<'EOF'
#!/bin/sh
if [ "${CURL_EXIT:-0}" -ne 0 ]; then
  echo "curl: simulated failure" >&2
  exit "${CURL_EXIT}"
fi
cat "${FIXTURE}"
EOF
chmod +x "${STUB_DIR}/curl"
# grep stub used only for the scan-error case: exit 2 (grep's "error" status,
# also what an absent grep effectively means for the pipeline).
cat >"${STUB_DIR}/grep" <<'EOF'
#!/bin/sh
exit 2
EOF
chmod +x "${STUB_DIR}/grep"

# --- test harness -----------------------------------------------------------
FAILURES=0

# run_case <name> <fixture> <curl_exit> <use_grep_stub:0|1> <expect_block:0|1> [expect_substr]
run_case() {
  local name=$1 fixture=$2 curl_exit=$3 use_grep_stub=$4 expect_block=$5 expect_substr=${6:-}
  local path="${STUB_DIR}"
  # Only shadow grep for the explicit scan-error case; other cases use real grep.
  if [[ "${use_grep_stub}" == "1" ]]; then
    path="${STUB_DIR}"
  else
    path="${STUB_DIR}/curl_only"
    mkdir -p "${path}"
    ln -sf "${STUB_DIR}/curl" "${path}/curl"
  fi

  local out rc
  set +e
  out=$(PATH="${path}:${PATH}" FIXTURE="${WORK_DIR}/${fixture}" CURL_EXIT="${curl_exit}" \
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
# name                         fixture             curl grep block substr
run_case "block: 2 PVs compact JSON"   two_compact.json   0 0 1 "found 2"
run_case "block: 2 PVs pretty JSON"    two_pretty.json    0 0 1 "found 2"
run_case "allow: no PVs"               empty.json         0 0 0 "allowing uninstall"
run_case "allow: only other driver"    other_driver.json  0 0 0 "allowing uninstall"
run_case "block: apiserver fetch fails" empty.json        22 0 1
run_case "block: scan/tool error"      two_compact.json   0 1 1

echo
if [[ "${FAILURES}" -ne 0 ]]; then
  echo "Pre-delete guard verification FAILED (${FAILURES} case(s))."
  exit 1
fi
echo "Pre-delete guard verification succeeded!"
