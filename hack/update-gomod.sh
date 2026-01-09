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

VERSION=${1#"v"}
if [[ -z "${VERSION}" ]]; then
    echo "Please specify the Kubernetes version: e.g."
    echo "./update-gomod.sh v1.32.11"
    exit 1
fi

# Determine which imports must be replaced
gomod_url="https://raw.githubusercontent.com/kubernetes/kubernetes/v${VERSION}/go.mod"
gomod_content=$(curl -sSf "${gomod_url}") || {
    echo "Failed to fetch go.mod from ${gomod_url}"
    exit 1
}
mapfile -t MODS < <(
    echo "${gomod_content}" |
    sed -n 's|.*k8s.io/\(.*\) => ./staging/src/k8s.io/.*|k8s.io/\1|p'
)

# Add replace statements to the go.mod file with the version Kubernetes is using for them.
for MOD in "${MODS[@]}"; do
    echo "${MOD}"
    V=$(
        go mod download -json "${MOD}@kubernetes-${VERSION}" |
        sed -n 's|.*"Version": "\(.*\)".*|\1|p'
    )
    echo "${V}"
    go mod edit "-replace=${MOD}=${MOD}@${V}"
done

go get "k8s.io/kubernetes@v${VERSION}"
go mod download
go mod tidy
go mod vendor
