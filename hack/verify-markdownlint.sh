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

if ! command -v markdownlint >/dev/null 2>&1; then
  echo "Cannot find markdownlint. Installing markdownlint-cli to /tmp..."
  npm install --prefix /tmp/markdownlint-cli markdownlint-cli@0.48.0
  export PATH="/tmp/markdownlint-cli/node_modules/.bin:${PATH}"
fi

echo "Verifying markdownlint"

# Collect markdown files tracked by git, excluding vendor only
FILES=()
while IFS= read -r f; do
  FILES+=("${f}")
done < <(git ls-files '*.md' ':!vendor/')

if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "No markdown files found."
  exit 0
fi

RES=0
if ! markdownlint "${FILES[@]}"; then
  RES=1
fi

if [[ "${RES}" -eq 0 ]]; then
  echo "Congratulations! All Markdown files have been linted."
fi
exit "${RES}"
