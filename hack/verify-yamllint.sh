#!/usr/bin/env bash

# Copyright 2020 The Kubernetes Authors.
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

readonly YAMLLINT_VERSION="1.33.0"
readonly YAMLLINT_REQUIREMENTS="${ROOT}/hack/tools/yamllint-requirements.txt"
requirements_hash=$(sha256sum "${YAMLLINT_REQUIREMENTS}")
requirements_hash=${requirements_hash%% *}
python_abi=$("${PYTHON_BIN}" -c 'import sysconfig; print(sysconfig.get_config_var("SOABI"))')
readonly YAMLLINT_DIR="${ROOT}/_output/tools/yamllint/${YAMLLINT_VERSION}/${requirements_hash}/${python_abi}"
readonly YAMLLINT_PACKAGES="${YAMLLINT_DIR}/packages"

installed_version=""
if [[ -d "${YAMLLINT_PACKAGES}" ]]; then
  installed_version=$(PYTHONPATH="${YAMLLINT_PACKAGES}" "${PYTHON_BIN}" -S -m yamllint --version 2>&1) || installed_version=""
fi
if [[ "${installed_version}" != "yamllint ${YAMLLINT_VERSION}" ]]; then
  PYTHON_BIN=$("${ROOT}/hack/ensure-python.sh" --pip)

  rm -rf "${YAMLLINT_DIR}"
  mkdir -p "${YAMLLINT_PACKAGES}"
  echo "Installing yamllint ${YAMLLINT_VERSION} ..."
  "${PYTHON_BIN}" -m pip install \
    --disable-pip-version-check \
    --require-hashes \
    --only-binary=:all: \
    --target "${YAMLLINT_PACKAGES}" \
    --requirement "${YAMLLINT_REQUIREMENTS}"
fi

run_yamllint() {
  PYTHONPATH="${YAMLLINT_PACKAGES}" "${PYTHON_BIN}" -S -m yamllint "$@"
}

echo "Checking with yamllint ${YAMLLINT_VERSION} ..."
paths_output=$(find docs deploy test .github/workflows -name '*.yaml' -o -name '*.yml')
paths=()
if [[ -n "${paths_output}" ]]; then
  mapfile -t paths <<<"${paths_output}"
fi
paths+=(.golangci.yaml .yamllint.yaml)
for path in "${paths[@]}"
do
    echo "checking yamllint under path: ${path} ..."
    if ! output=$(run_yamllint --strict -f parsable "${path}" 2>&1); then
        echo "yaml files under ${path} are not linted, failed with: "
        echo "${output}"
        exit 1
    fi
done

echo "Congratulations! All Yaml files have been linted."
