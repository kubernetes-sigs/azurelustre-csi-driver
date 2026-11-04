#!/usr/bin/env bash

# Copyright 2019 The Kubernetes Authors.
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

# cd to the root path
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${ROOT}"

readonly MISSPELL_VERSION="v0.3.4"
readonly MISSPELL_DIR="${ROOT}/_output/tools/misspell/${MISSPELL_VERSION}"
readonly MISSPELL_BIN="${MISSPELL_DIR}/misspell"

# create a temporary directory
TMP_DIR=$(mktemp -d)

trap 'echo "Cleaning up..."; rm -rf "${TMP_DIR}"' EXIT

installed_version=""
if [[ -x "${MISSPELL_BIN}" ]]; then
  installed_version=$("${MISSPELL_BIN}" -v 2>&1) || installed_version=""
fi
if [[ "${installed_version}" != "${MISSPELL_VERSION}" ]]; then
  rm -rf "${MISSPELL_DIR}"
  mkdir -p "${MISSPELL_DIR}"
  echo "Installing misspell ${MISSPELL_VERSION} ..."
  GOBIN="${MISSPELL_DIR}" go install -ldflags "-X main.version=${MISSPELL_VERSION}" "github.com/client9/misspell/cmd/misspell@${MISSPELL_VERSION}"
fi

# check spelling
RES=0
echo "Checking spelling with misspell ${MISSPELL_VERSION}..."
ERROR_LOG="${TMP_DIR}/errors.log"
git ls-files -z ':!vendor/**' | xargs -0 -- "${MISSPELL_BIN}" > "${ERROR_LOG}"
if [[ -s "${ERROR_LOG}" ]]; then
  sed 's/^/error: /' "${ERROR_LOG}" # add 'error' to each line to highlight in e2e status
  echo "Found spelling errors!"
  RES=1
fi
exit "${RES}"
