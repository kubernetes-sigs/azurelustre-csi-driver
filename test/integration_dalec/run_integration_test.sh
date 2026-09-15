#!/usr/bin/env bash

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

# This script runs in the "tester" sidecar container, which carries a prebuilt
# `csc` client. The driver under test runs in a separate container in the same
# pod; the two share the CSI socket via an emptyDir mounted at /csi. This keeps
# the shipped driver image (azlinux3/jammy/noble) unmodified -- no package
# manager, Go toolchain, or csc is installed into it -- so the same harness
# works across all distro variants, including the distroless Azure Linux 3 image.

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

volname="citest-$(date +%s)"
readonly volname
readonly volsize="2147483648"
readonly endpoint="unix:///csi/csi.sock"
readonly target_path="/tmp/target_path"
readonly lustre_fs_ip=1.2.3.4

mkdir -p "${target_path}"

# csc is baked into this sidecar image. Verify it is present and executable up
# front so a broken/missing image fails immediately with a clear message
command -v csc >/dev/null 2>&1 || { echo "ERROR: csc not found in tester image" >&2; exit 1; }

# Wait for the driver container to create the CSI socket on the shared volume.
echo "$(date -u) Waiting for CSI socket ${endpoint}"
for _ in $(seq 1 60); do
    if [[ -S /csi/csi.sock ]]; then
        break
    fi
    sleep 1
done

if [[ ! -S /csi/csi.sock ]]; then
    echo "ERROR: CSI socket /csi/csi.sock did not appear within 60s" >&2
    exit 1
fi

echo "====: $(date -u) Starting integration test"
export X_CSI_DEBUG=true
echo "====: $(date -u) Create volume test:"
value="$(csc controller new --endpoint "${endpoint}" \
                            --cap MULTI_NODE_MULTI_WRITER,mount,,, \
                            "${volname}" \
                            --req-bytes "${volsize}" \
                            --params fs-name=lustrefs,mgs-ip-address="${lustre_fs_ip}")"

volumeid="$(echo "${value}" | awk '{print $1}' | sed 's/"//g')"
echo "====: $(date -u) Volume ID is ${volumeid}"

echo "====: $(date -u) Validate volume capabilities test:"
csc controller validate-volume-capabilities --endpoint "${endpoint}" \
                                            --cap MULTI_NODE_MULTI_WRITER,mount,,, \
                                            "${volumeid}"

echo "====: $(date -u) Node publish volume test:"  # Requires routing to amlfs
csc node publish --endpoint "${endpoint}" \
                 --cap MULTI_NODE_MULTI_WRITER,mount,,, \
                 --target-path "${target_path}" \
                 --vol-context "fs-name=lustrefs,mgs-ip-address=${lustre_fs_ip}" \
                 "${volumeid}"

echo "====: $(date -u) stats test:"
csc node stats --endpoint "${endpoint}" "${volumeid}:${target_path}"

echo "====: $(date -u) Node unpublish volume test:"  # Requires routing to amlfs
csc node unpublish --endpoint "${endpoint}" \
                   --target-path "${target_path}" \
                   "${volumeid}"

echo "====: $(date -u) Delete volume test:"
csc controller del --endpoint "${endpoint}" "${volumeid}"

echo "====: $(date -u) Identity test:"
csc identity plugin-info --endpoint "${endpoint}"

echo "====: $(date -u) Node get info test:"
csc node get-info --endpoint "${endpoint}"

echo "$(date -u) Integration test is completed."
