#!/bin/bash

# Copyright 2025 The Kubernetes Authors.
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

# Wrapper script that checks for a custom entrypoint mounted via ConfigMap.
# If a custom entrypoint exists at /app/custom-entrypoint/entrypoint.sh,
# it will be used instead of the built-in /app/entrypoint.sh.

CUSTOM_ENTRYPOINT="/app/custom-entrypoint/entrypoint.sh"

if [[ -x "${CUSTOM_ENTRYPOINT}" ]]; then
    echo "$(date -u) Using custom entrypoint: ${CUSTOM_ENTRYPOINT}"
    exec "${CUSTOM_ENTRYPOINT}" "$@"
fi

exec /app/entrypoint.sh "$@"
