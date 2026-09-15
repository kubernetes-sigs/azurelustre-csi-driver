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

# shellcheck disable=SC2312 # command -v prints nothing on failure; -z captures both signals
if [[ -z "$(command -v yamllint)" ]]; then
  apt update && apt install yamllint -y
fi

for path in $(find docs deploy test .github/workflows -name '*.yaml' -o -name '*.yml') .golangci.yaml .yamllint.yaml
do
    echo "checking yamllint under path: ${path} ..."
    if ! output=$(yamllint --strict -f parsable "${path}" 2>&1); then
        echo "yaml files under ${path} are not linted, failed with: "
        echo "${output}"
        exit 1
    fi
done

echo "Congratulations! All Yaml files have been linted."
