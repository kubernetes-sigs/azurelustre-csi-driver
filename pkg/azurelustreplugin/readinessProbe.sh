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

# readinessProbe.sh checks LNet health for the loader and driver. The driver's
# worker also binds its result to a supported resident client and current policy;
# loader completion is not a prerequisite for driver host-runtime readiness.
# This is not a liveness check: recoverable LNet drift must not restart CSI.
# Legacy startup additionally uses this as the loader's blocking startupProbe.

set -euo pipefail

# shellcheck source=pkg/azurelustreplugin/startupState.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/startupState.sh"

if [[ -n "${AZURELUSTRE_NODE_STARTUP_DIR:-}" ]]; then
    if [[ "${AZURELUSTRE_CSI_ROLE:-}" == driver ]]; then
        # The driver-owned worker checks runtime first, then publishes this
        # marker. Kubelet checks must wait for that handoff, not just packages.
        if [[ "${1:-}" != --runtime ]]; then
            driver_startup_ready || { echo "Driver runtime observation is unavailable or stale"; exit 1; }
        fi
        for tool in mount umount mount.lustre lnetctl; do
            command -v "${tool}" >/dev/null || exit 1
        done
    else
        loader_startup_ready || { echo "Loader setup is incomplete"; exit 1; }
    fi
fi

# LNet must respond.
if ! lnetctl net show >/dev/null 2>&1; then
    echo "LNet not available or not configured"
    exit 1
fi

# At least one NID must be configured.
nid_count=$(lnetctl net show 2>/dev/null | grep -c "nid:") || true
if [[ "${nid_count}" -eq 0 ]]; then
    echo "No LNet NIDs configured"
    exit 1
fi

# The ping subcommand must be available.
if ! lnetctl ping --help >/dev/null 2>&1; then
    echo "LNet ping functionality not available"
    exit 1
fi

# Self-ping the first non-loopback NID. "sed -n 1s" rather than "head -1 | sed":
# head closes the pipe on its first line, which under pipefail can surface the
# producer's SIGPIPE as a false failure.
first_nid=$(lnetctl net show 2>/dev/null | grep "nid:" | grep -v "@lo" | sed -n '1s/.*nid: \([^ ]*\).*/\1/p' || echo "")
if [[ -z "${first_nid}" ]]; then
    echo "Unable to determine LNet NID for self-ping test"
    exit 1
fi
# Bounded below the tightest probe timeoutSeconds (the loader startupProbe's 5s)
# so a stuck self-ping fails with the message below instead of kubelet killing
# the exec and reporting only a timeout.
if ! timeout 3 lnetctl ping "${first_nid}" >/dev/null 2>&1; then
    echo "LNet self-ping test failed for NID: ${first_nid}"
    exit 1
fi

# At least one interface must be in the 'up' state.
up_interfaces=$(lnetctl net show 2>/dev/null | grep -c "status: up") || true
if [[ "${up_interfaces}" -eq 0 ]]; then
    echo "No LNet interfaces in 'up' state"
    exit 1
fi

echo "LNet readiness checks passed"
exit 0