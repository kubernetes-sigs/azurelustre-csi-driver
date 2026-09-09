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

# readinessProbe.sh - LNet health check for the Azure Lustre loader sidecar.
# Used as the loader sidecar's startupProbe and readinessProbe: the startupProbe
# gates the driver and registrar containers on LNet being up, and the
# readinessProbe feeds pod readiness. It is deliberately NOT the sidecar's
# livenessProbe -- that is a narrower "is the lnet module still loaded" check,
# so recoverable LNet config drift (which the reconcile loop repairs in place)
# cannot trigger a restart.
# The CSI driver container's own health is covered by the livenessprobe sidecar
# (gRPC Probe over the socket) and node-driver-registrar, so it needs no script.

set -euo pipefail

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