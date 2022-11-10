#!/bin/bash

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

echo "Installing go and ginkgo"
curl -Lo ${REPO_ROOT_PATH}/go1.19.2.linux-amd64.tar.gz https://go.dev/dl/go1.19.2.linux-amd64.tar.gz
tar -zxf go1.19.2.linux-amd64.tar.gz -C ${REPO_ROOT_PATH}/
export GOBIN=${REPO_ROOT_PATH}/go/bin
export PATH=${GOBIN}:${PATH}
go install github.com/onsi/ginkgo/v2/ginkgo@1.2.0

echo "Downloading latest kubectl"
curl -Lo ${REPO_ROOT_PATH}/kubectl "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod a+x ${REPO_ROOT_PATH}/kubectl
export PATH=$(pwd):${PATH}

${REPO_ROOT_PATH}/test/external-e2e/run.sh
