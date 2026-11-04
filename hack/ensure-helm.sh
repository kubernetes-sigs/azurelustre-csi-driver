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

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
readonly HELM_VERSION="v3.21.0"
readonly HELM_SHA256_AMD64="0093eb572e3d2380f094df162ddb525e219249de88957afe24cfbb19632acd36"
readonly HELM_SHA256_ARM64="8de5a0c9a47431e59fd560e91e0779c8cf9316c383da7efb84128a4c339ecb2d"
readonly HELM_DIR="${ROOT}/_output/tools/helm/${HELM_VERSION}"
readonly HELM_BIN="${HELM_DIR}/helm"

installed_version=""
if [[ -x "${HELM_BIN}" ]]; then
  installed_version=$("${HELM_BIN}" version --short 2>&1) || installed_version=""
fi
if [[ "${installed_version}" != "${HELM_VERSION}"+* ]]; then
  arch=$(uname -m)
  case "${arch}" in
    x86_64)
      arch="amd64"
      checksum="${HELM_SHA256_AMD64}"
      ;;
    aarch64 | arm64)
      arch="arm64"
      checksum="${HELM_SHA256_ARM64}"
      ;;
    *)
      echo "Unsupported architecture: ${arch}; Helm supports x86_64 and arm64." >&2
      exit 1
      ;;
  esac

  temp_dir=$(mktemp -d)
  trap 'rm -rf "${temp_dir}"' EXIT
  artifact="helm-${HELM_VERSION}-linux-${arch}.tar.gz"

  "${ROOT}/hack/tools/download-verified.sh" \
    "https://get.helm.sh/${artifact}" \
    "${checksum}" \
    "${temp_dir}/${artifact}"
  tar -xzf "${temp_dir}/${artifact}" -C "${temp_dir}"
  mkdir -p "${HELM_DIR}"
  install -m 0755 "${temp_dir}/linux-${arch}/helm" "${HELM_BIN}"
fi

printf '%s\n' "${HELM_BIN}"
