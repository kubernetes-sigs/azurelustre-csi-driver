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
cd "${ROOT}"
PYTHON_BIN=$("${ROOT}/hack/ensure-python.sh")

readonly CODESPELL_VERSION="2.2.6"
readonly CODESPELL_URL="https://files.pythonhosted.org/packages/46/e0/5437cc96b74467c4df6e13b7128cc482c48bb43146fb4c11cf2bcd604e1f/codespell-2.2.6-py3-none-any.whl"
readonly CODESPELL_SHA256="9ee9a3e5df0990604013ac2a9f22fa8e57669c827124a2e961fe8a1da4cacc07"
readonly CODESPELL_DIR="${ROOT}/_output/tools/codespell/${CODESPELL_VERSION}"
readonly CODESPELL_PACKAGES="${CODESPELL_DIR}/packages"
readonly CODESPELL_BIN="${CODESPELL_PACKAGES}/bin/codespell"

temp_dir=$(mktemp -d)
trap 'rm -rf "${temp_dir}"' EXIT

installed_version=""
if [[ -f "${CODESPELL_BIN}" ]]; then
  installed_version=$(PYTHONPATH="${CODESPELL_PACKAGES}" "${PYTHON_BIN}" -S "${CODESPELL_BIN}" --version 2>&1) || installed_version=""
fi
if [[ "${installed_version}" != "${CODESPELL_VERSION}" ]]; then
  PYTHON_BIN=$("${ROOT}/hack/ensure-python.sh" --pip)

  wheel=$(basename "${CODESPELL_URL}")

  rm -rf "${CODESPELL_DIR}"
  mkdir -p "${CODESPELL_PACKAGES}"
  "${ROOT}/hack/tools/download-verified.sh" \
    "${CODESPELL_URL}" \
    "${CODESPELL_SHA256}" \
    "${temp_dir}/${wheel}"
  echo "Installing codespell ${CODESPELL_VERSION} ..."
  "${PYTHON_BIN}" -m pip install \
    --disable-pip-version-check \
    --target "${CODESPELL_PACKAGES}" \
    "${temp_dir}/${wheel}"
fi

git ls-files -z >"${temp_dir}/files"
FILES=()
while IFS= read -r -d '' file; do
  case "${file}" in
    .git | .git/* | */.git | */.git/* | .jj | .jj/* | */.jj | */.jj/*) continue ;;
    vendor/* | _output/* | *.jpg | *.png | *.sum | *.svg) continue ;;
    *) FILES+=("${file}") ;;
  esac
done <"${temp_dir}/files"

if [[ "${#FILES[@]}" -eq 0 ]]; then
  echo "No files found to check with codespell." >&2
  exit 1
fi

echo "Checking spelling and filenames with codespell ${CODESPELL_VERSION}..."
PYTHONPATH="${CODESPELL_PACKAGES}" \
  "${PYTHON_BIN}" -S "${CODESPELL_BIN}" \
    --check-hidden \
    --check-filenames \
    --count \
    --ignore-words-list "AKS,aks,ro,NotIn,decorder" \
    "${FILES[@]}"