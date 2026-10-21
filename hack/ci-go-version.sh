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

if [[ $# -gt 1 ]]; then
  echo "Usage: $0 [go.mod]" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
module_file="${1:-${ROOT}/go.mod}"
go_version=$(awk '$1 == "go" { sub(/\r$/, "", $2); print $2 }' "${module_file}")
if [[ ! "${go_version}" =~ ^([0-9]+)\.([0-9]+)(\.[0-9]+)?$ ]]; then
  printf 'Expected one stable Go version in %s; found %q\n' "${module_file}" "${go_version}" >&2
  exit 1
fi

printf '%s.%s.x\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
