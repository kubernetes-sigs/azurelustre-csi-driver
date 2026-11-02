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
readonly YQ_VERSION="v4.53.3"
readonly YQ_SHA256_AMD64="fa52a4e758c63d38299163fbdd1edfb4c4963247918bf9c1c5d31d84789eded4"
readonly YQ_SHA256_ARM64="578648e463a11c1b6db6010cbf41eafed6bee79466fcffa1bb446672cf7945ea"
readonly YQ_DIR="${ROOT}/_output/tools/yq/${YQ_VERSION}"
readonly YQ_BIN="${YQ_DIR}/yq"

installed_version=""
if [[ -x "${YQ_BIN}" ]]; then
  installed_version=$("${YQ_BIN}" --version 2>&1) || installed_version=""
fi
if [[ "${installed_version}" != *"version ${YQ_VERSION}" ]]; then
  arch=$(uname -m)
  case "${arch}" in
    x86_64)
      arch="amd64"
      checksum="${YQ_SHA256_AMD64}"
      ;;
    aarch64 | arm64)
      arch="arm64"
      checksum="${YQ_SHA256_ARM64}"
      ;;
    *)
      echo "Unsupported architecture: ${arch}, must be x86_64 or aarch64" >&2
      exit 1
      ;;
  esac

  temp_dir=$(mktemp -d)
  trap 'rm -rf "${temp_dir}"' EXIT
  artifact="yq_linux_${arch}"

  "${ROOT}/hack/tools/download-verified.sh" \
    "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/${artifact}" \
    "${checksum}" \
    "${temp_dir}/${artifact}"
  mkdir -p "${YQ_DIR}"
  install -m 0755 "${temp_dir}/${artifact}" "${YQ_BIN}"
fi

printf '%s\n' "${YQ_BIN}"
