#!/usr/bin/env bash

# Copyright 2018 The Kubernetes Authors.
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

if [[ "$(golangci-lint version --short 2>/dev/null)" != "2."* ]]; then
  echo "golangci-lint not found or not v2.x. Installing golangci-lint..."
  gopath=$(go env GOPATH)
  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b "${gopath}/bin" v2.7.2
  export PATH=${PATH}:"${gopath}/bin"
fi

echo "Verifying golangci-lint"

golangci-lint run --timeout=10m

echo "Congratulations! Lint check completed for all Go source files."
