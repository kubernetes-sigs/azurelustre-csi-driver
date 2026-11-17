#!/bin/bash

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

function startup_identity() {
  local suffix="${CLIENT_SHA_SUFFIX:?}"
  printf '%s %s %s_%s' "$(cat /proc/sys/kernel/random/boot_id)" "${POD_UID:?}" \
    "${LUSTRE_VERSION:?}" "${suffix//-/_}"
}

function begin_loader_startup() {
  [[ -n "${AZURELUSTRE_NODE_STARTUP_DIR:-}" ]] || return 0
  [[ "${AZURELUSTRE_NODE_STARTUP_DIR}" == /* && "${AZURELUSTRE_NODE_STARTUP_DIR}" != / ]]
  [[ -d "${AZURELUSTRE_NODE_STARTUP_DIR}" ]]
  LOADER_STARTUP_RECORD="$(startup_identity) $(cat /proc/sys/kernel/random/uuid) $(date +%s)"
  printf '%s\n' "${LOADER_STARTUP_RECORD}" > "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-started.tmp"
  mv "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-started.tmp" "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-started"
  rm -f "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-ready"
}

function finish_loader_startup() {
  [[ -n "${AZURELUSTRE_NODE_STARTUP_DIR:-}" ]] || return 0
  printf '%s\n' "${LOADER_STARTUP_RECORD:?}" > "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-ready.tmp"
  mv "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-ready.tmp" "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-ready"
}

function clear_loader_startup() {
  [[ -n "${AZURELUSTRE_NODE_STARTUP_DIR:-}" ]] || return 0
  rm -f "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-ready"
}

function loader_startup_ready() {
  local started ready
  started=$(cat "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-started") || return 1
  ready=$(cat "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-ready") || return 1
  [[ "${started}" == "$(startup_identity) "* && "${started}" == "${ready}" ]]
}

function driver_startup_ready() {
  local boot pod client loaded fingerprint observed extra current_fingerprint now
  read -r boot pod client loaded fingerprint observed extra < "${AZURELUSTRE_NODE_STARTUP_DIR}/driver-ready" || return 1
  [[ "${boot} ${pod} ${client}" == "$(startup_identity)" && -z "${extra}" ]] || return 1
  [[ "${observed}" =~ ^[0-9]{1,10}$ && "${fingerprint}" =~ ^[a-f0-9]{64}$ ]] || return 1
  [[ -n "${loaded}" && "${loaded}" == "$(cat /sys/module/lustre/version)" ]] || return 1
  current_fingerprint=$(sha256sum /etc/azurelustre/compatibility/policy.yaml) || return 1
  [[ "${fingerprint}" == "${current_fingerprint%% *}" ]] || return 1
  now=$(date +%s)
  # Match the node fact lease's 90-second maximum observation age.
  (( now >= observed && now - observed <= 90 ))
}

function loader_startup_live() {
  # A retirement request intentionally leaves modules absent and the loader
  # drained. Keep its existing identity-checked health path.
  if [[ -n "${AZURELUSTRE_TEARDOWN_DIR:-}" && -e "${AZURELUSTRE_TEARDOWN_DIR}/request" ]]; then
    /app/azurelustreplugin --experimental-node-teardown-dir="${AZURELUSTRE_TEARDOWN_DIR}" --experimental-node-teardown-health
    return
  fi
  if loader_startup_ready; then
    [[ -d /sys/module/lnet ]]
    return
  fi
  local boot pod client generation started extra now
  read -r boot pod client generation started extra < "${AZURELUSTRE_NODE_STARTUP_DIR}/loader-started" || return 1
  [[ "${boot} ${pod} ${client}" == "$(startup_identity)" && -n "${generation}" && -z "${extra}" ]] || return 1
  [[ "${started}" =~ ^[0-9]{1,10}$ ]] || return 1
  now=$(date +%s)
  # Preserve the old ten-minute setup allowance without blocking CSI startup.
  (( now >= started && now - started < 600 ))
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  set -euo pipefail
  [[ "${1:-}" == live ]]
  loader_startup_live
fi
