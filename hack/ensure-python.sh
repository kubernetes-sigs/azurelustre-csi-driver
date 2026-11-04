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

if [[ $# -gt 1 || ( $# -eq 1 && "${1:-}" != "--pip" ) ]]; then
  echo "Usage: $0 [--pip]" >&2
  exit 2
fi

if ! PYTHON_BIN=$(command -v python3); then
  echo "Python 3.8 or later is required for verification. Install it and make python3 available on PATH, or activate a supported Python environment." >&2
  exit 1
fi

if ! "${PYTHON_BIN}" -c 'import sys; sys.exit(sys.version_info < (3, 8))'; then
  echo "Python 3.8 or later is required for verification. Upgrade python3 on PATH, or activate a supported Python environment." >&2
  exit 1
fi

if [[ "${1:-}" == "--pip" ]] && ! "${PYTHON_BIN}" -m pip --version >/dev/null 2>&1; then
  echo "Python 3 pip is required to install verification tools. Install pip for python3 (python3-pip on Ubuntu), or activate a Python 3 environment with pip." >&2
  exit 1
fi

printf '%s\n' "${PYTHON_BIN}"
