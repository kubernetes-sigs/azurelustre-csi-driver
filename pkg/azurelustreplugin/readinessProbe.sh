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

# readinessProbe.sh - Health check script for Azure Lustre CSI driver
# This script performs direct LNet readiness validation

set -euo pipefail

# Check if this is a controller pod (no Lustre client installation required)
INSTALL_LUSTRE_CLIENT=${AZURELUSTRE_CSI_INSTALL_LUSTRE_CLIENT:-"yes"}

if [[ "${INSTALL_LUSTRE_CLIENT}" == "no" ]]; then
    echo "Controller pod detected - reporting ready (skipping Lustre checks)"
    exit 0
fi

echo "Node pod detected - performing Lustre-specific readiness checks"

# Check if CSI socket exists and is accessible
SOCKET_PATH=${CSI_ENDPOINT#unix://}

if [[ ! -S "${SOCKET_PATH}" ]]; then
    echo "CSI socket not found: ${SOCKET_PATH}"
    exit 1
fi

# Check if LNet is properly configured and operational
# This replicates the logic from CheckLustreReadiness()

# Check if LNet NIDs are valid and available
if ! lnetctl net show >/dev/null 2>&1; then
    echo "LNet not available or not configured"
    exit 1
fi

# Check if we have any NIDs configured
NID_COUNT=$(lnetctl net show 2>/dev/null | grep -c "nid:")
if [[ "${NID_COUNT}" -eq 0 ]]; then
    echo "No LNet NIDs configured"
    exit 1
fi

# Check LNet self-ping functionality
if ! lnetctl ping --help >/dev/null 2>&1; then
    echo "LNet ping functionality not available"
    exit 1
fi

# Get the first available NID for self-ping test (exclude loopback)
FIRST_NID=$(lnetctl net show 2>/dev/null | grep "nid:" | grep -v "@lo" | head -1 | sed 's/.*nid: \([^ ]*\).*/\1/' || echo "")
if [[ -z "${FIRST_NID}" ]]; then
    echo "Unable to determine LNet NID for self-ping test"
    exit 1
fi

# Perform self-ping test with timeout
if ! timeout 10 lnetctl ping "${FIRST_NID}" >/dev/null 2>&1; then
    echo "LNet self-ping test failed for NID: ${FIRST_NID}"
    exit 1
fi

# Check if LNet interfaces are operational
# Verify we have at least one interface in 'up' state
UP_INTERFACES=$(lnetctl net show 2>/dev/null | grep -c "status: up")
if [[ "${UP_INTERFACES}" -eq 0 ]]; then
    echo "No LNet interfaces in 'up' state"
    exit 1
fi

echo "All Lustre readiness checks passed"
exit 0