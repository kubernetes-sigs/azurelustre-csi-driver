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

set -o errexit
set -o pipefail
set -o nounset
set -o xtrace

kubernetesVersion="v$(az aks list | jq -r ".[0].currentKubernetesVersion")"
export kubernetesVersion
echo "Current kubernetes version is ${kubernetesVersion}"

echo "Downloading kubectl ${kubernetesVersion}"
# shellcheck disable=SC2154 # REPO_ROOT_PATH: expected env var exported by calling script (start-long-haul.sh)
curl -fLo "${REPO_ROOT_PATH}/kubectl" "https://dl.k8s.io/release/${kubernetesVersion}/bin/linux/amd64/kubectl"
chmod a+x "${REPO_ROOT_PATH}/kubectl"
PATH="${REPO_ROOT_PATH}:${PATH}"
export PATH

# shellcheck disable=SC2154 # LustreFSName is an expected env var from the pipeline
export LUSTRE_FS_NAME="${LustreFSName}"
# shellcheck disable=SC2154 # LustreFSIP is an expected env var from the pipeline
export LUSTRE_MGS_IP="${LustreFSIP}"

"${REPO_ROOT_PATH}/test/external-e2e/run.sh"
