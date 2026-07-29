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

TOOL_VERSION="v1.7.12"

# cd to the root path
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${ROOT}"

# create a temporary directory
TMP_DIR=$(mktemp -d)

# cleanup
exitHandler() {
  echo "Cleaning up..."
  rm -rf "${TMP_DIR}"
}
trap exitHandler EXIT

if [[ -z "$(command -v actionlint)" ]]; then
  echo "Cannot find actionlint. Installing actionlint..."
  # perform go install in a temp dir as we are not tracking this version in a go module
  # if we do the go install in the repo, it will create / update a go.mod and go.sum
  cd "${TMP_DIR}"
  GO111MODULE=on GOBIN="${TMP_DIR}" go install "github.com/rhysd/actionlint/cmd/actionlint@${TOOL_VERSION}"
  export PATH="${TMP_DIR}:${PATH}"
fi
cd "${ROOT}"

# actionlint will additionally invoke shellcheck on `run:` blocks if it is on
# PATH. pyflakes checks `shell: python` blocks, which we don't use, so disable
# it explicitly (via empty command name) to keep -verbose output clean.
echo "Running actionlint..."
actionlint -pyflakes= -verbose
