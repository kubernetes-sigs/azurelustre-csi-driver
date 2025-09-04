#!/bin/bash

# readinessProbe.sh - Health check script for Azure Lustre CSI driver
# This script performs direct LNet readiness validation

set -euo pipefail

# Check if this is a controller pod (no Lustre client installation required)
INSTALL_LUSTRE_CLIENT=${AZURELUSTRE_CSI_INSTALL_LUSTRE_CLIENT:-"yes"}

if [[ "$INSTALL_LUSTRE_CLIENT" == "no" ]]; then
    echo "Controller pod detected - reporting ready (skipping Lustre checks)"
    exit 0
fi

echo "Node pod detected - performing Lustre-specific readiness checks"

# Check if CSI socket exists and is accessible
CSI_SOCKET=${CSI_ENDPOINT:-"unix:///csi/csi.sock"}
SOCKET_PATH=$(echo "$CSI_SOCKET" | sed 's|unix://||')

if [[ ! -S "$SOCKET_PATH" ]]; then
    echo "CSI socket not found: $SOCKET_PATH"
    exit 1
fi

# Check if LNet is properly configured and operational
# This replicates the logic from CheckLustreReadiness()

# 1. Check if LNet NIDs are valid and available
if ! lnetctl net show >/dev/null 2>&1; then
    echo "LNet not available or not configured"
    exit 1
fi

# 2. Check if we have any NIDs configured
NID_COUNT=$(lnetctl net show 2>/dev/null | grep -c "nid:" || echo "0")
if [[ "$NID_COUNT" -eq 0 ]]; then
    echo "No LNet NIDs configured"
    exit 1
fi

# 3. Check LNet self-ping functionality
if ! lnetctl ping --help >/dev/null 2>&1; then
    echo "LNet ping functionality not available"
    exit 1
fi

# Get the first available NID for self-ping test
FIRST_NID=$(lnetctl net show 2>/dev/null | grep "nid:" | head -1 | awk '{print $2}' || echo "")
if [[ -z "$FIRST_NID" ]]; then
    echo "Unable to determine LNet NID for self-ping test"
    exit 1
fi

# Perform self-ping test with timeout
if ! timeout 10 lnetctl ping "$FIRST_NID" >/dev/null 2>&1; then
    echo "LNet self-ping test failed for NID: $FIRST_NID"
    exit 1
fi

# 4. Check if LNet interfaces are operational
# Verify we have at least one interface in 'up' state
UP_INTERFACES=$(lnetctl net show 2>/dev/null | grep -c "state: up" || echo "0")
if [[ "$UP_INTERFACES" -eq 0 ]]; then
    echo "No LNet interfaces in 'up' state"
    exit 1
fi

echo "All Lustre readiness checks passed"
exit 0