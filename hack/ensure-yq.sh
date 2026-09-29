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

ensure_yq() {
  local install_dir=$1
  local version="v4.53.3"
  local installed_version=""

  if command -v yq >/dev/null 2>&1; then
    installed_version=$(yq --version 2>&1 || true)
    if [[ "${installed_version}" == *"version ${version}" ]]; then
      return
    fi
  fi

  local arch checksum
  arch=$(uname -m)
  case "${arch}" in
    x86_64)
      arch="amd64"
      checksum="fa52a4e758c63d38299163fbdd1edfb4c4963247918bf9c1c5d31d84789eded4"
      ;;
    aarch64 | arm64)
      arch="arm64"
      checksum="578648e463a11c1b6db6010cbf41eafed6bee79466fcffa1bb446672cf7945ea"
      ;;
    *)
      echo "Unsupported architecture: ${arch}, must be x86_64 or aarch64" >&2
      return 1
      ;;
  esac

  mkdir -p "${install_dir}"
  local yq_path="${install_dir}/yq"
  local url="https://github.com/mikefarah/yq/releases/download/${version}/yq_linux_${arch}"

  echo "Installing mikefarah yq ${version} for ${arch} ..."
  if ! curl -fsSL "${url}" -o "${yq_path}"; then
    echo "Failed to download ${url}" >&2
    return 1
  fi
  if ! printf '%s  %s\n' "${checksum}" "${yq_path}" | sha256sum --check --status; then
    echo "SHA-256 verification failed for ${url}" >&2
    rm -f "${yq_path}"
    return 1
  fi

  chmod +x "${yq_path}"
  export PATH="${install_dir}:${PATH}"
}
