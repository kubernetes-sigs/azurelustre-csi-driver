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

set -o errexit
set -o nounset
set -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${ROOT}"

if ! command -v node >/dev/null 2>&1; then
  echo "Node.js 20 or later is required for Markdown verification. Install it and make node available on PATH, or activate a supported Node.js environment." >&2
  exit 1
fi
if ! node -e 'process.exit(Number(process.versions.node.split(".")[0]) < 20 ? 1 : 0)'; then
  echo "Node.js 20 or later is required for Markdown verification. Upgrade node on PATH, or activate a supported Node.js environment." >&2
  exit 1
fi

readonly MARKDOWNLINT_VERSION="0.48.0"
readonly MARKDOWNLINT_DIR="${ROOT}/_output/tools/markdownlint/${MARKDOWNLINT_VERSION}"
readonly MARKDOWNLINT_BIN="${MARKDOWNLINT_DIR}/node_modules/.bin/markdownlint"

installed_version=""
if [[ -x "${MARKDOWNLINT_BIN}" ]]; then
  installed_version=$("${MARKDOWNLINT_BIN}" --version 2>&1) || installed_version=""
fi
if [[ "${installed_version}" != "${MARKDOWNLINT_VERSION}" ]]; then
  if ! command -v npm >/dev/null 2>&1; then
    echo "npm is required to install markdownlint. Install npm for Node.js 20 or later and make it available on PATH." >&2
    exit 1
  fi

  rm -rf "${MARKDOWNLINT_DIR}"
  mkdir -p "${MARKDOWNLINT_DIR}"
  echo "Installing markdownlint-cli ${MARKDOWNLINT_VERSION} ..."
  npm install \
    --prefix "${MARKDOWNLINT_DIR}" \
    --ignore-scripts \
    --no-audit \
    --no-fund \
    "markdownlint-cli@${MARKDOWNLINT_VERSION}"
fi

echo "Verifying markdownlint ${MARKDOWNLINT_VERSION}"

# Collect markdown files tracked by git, excluding vendor only
files_output=$(git ls-files '*.md' ':!vendor/')
FILES=()
if [[ -n "${files_output}" ]]; then
  while IFS= read -r f; do
    FILES+=("${f}")
  done <<< "${files_output}"
fi

if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "No markdown files found."
  exit 0
fi

RES=0
if ! "${MARKDOWNLINT_BIN}" "${FILES[@]}"; then
  RES=1
fi

if [[ "${RES}" -eq 0 ]]; then
  echo "Congratulations! All Markdown files have been linted."
fi
exit "${RES}"
