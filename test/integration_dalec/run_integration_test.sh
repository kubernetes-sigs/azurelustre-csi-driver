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

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

readonly volname="citest-$(date +%s)"
readonly volsize="2147483648"
readonly endpoint="unix:///csi/csi.sock"
readonly target_path="/tmp/target_path"
readonly lustre_fs_ip=1.2.3.4

mkdir -p $target_path

apt-get update
apt-get install -y --no-install-recommends kmod wget git ca-certificates lsb-release gpg curl
update-ca-certificates

curl -sL https://packages.microsoft.com/keys/microsoft.asc | gpg --dearmor | tee /etc/apt/trusted.gpg.d/microsoft.gpg > /dev/null
echo "deb [arch=amd64,arm64,armhf] https://packages.microsoft.com/ubuntu/22.04/prod jammy main" | tee /etc/apt/sources.list.d/amlfs.list
apt-get update

# This should match the version of Go that is used to build the CSI driver
apt install -y msft-golang=${MSFT_GOLANG_PKG_VER}

go version

lctl network up || true
echo "$(date -u) Enabled Lustre client kernel modules."

echo "$(date -u) Entering Lustre CSI driver"

echo "$(date -u) install csc"
go install github.com/dell/gocsi/csc@latest
export PATH=$PATH:/root/go/bin # add csc to path

mkdir /csi
echo "$(date -u) Exiting Lustre CSI driver"
nohup 2>&1 /app/azurelustreplugin --v=5 \
              --endpoint=${endpoint} \
              --enable-azurelustre-mock-mount \
	      --nodeid=integrationtestnode >csi.log &

sleep 5

echo "====: $(date -u) Exiting integration test"
export X_CSI_DEBUG=true
echo "====: $(date -u) Create volume test:"
value="$(csc controller new --endpoint "${endpoint}" \
                            --cap MULTI_NODE_MULTI_WRITER,mount,,, \
                            "${volname}" \
                            --req-bytes "${volsize}" \
                            --params fs-name=lustrefs,mgs-ip-address="${lustre_fs_ip}")"
sleep 5

volumeid="$(echo "$value" | awk '{print $1}' | sed 's/"//g')"
echo "====: $(date -u) Volume ID is $volumeid"

echo "====: $(date -u) Validate volume capabilities test:"
csc controller validate-volume-capabilities --endpoint "${endpoint}" \
                                            --cap MULTI_NODE_MULTI_WRITER,mount,,, \
                                            "$volumeid"

echo "====: $(date -u) stats test:"
csc node stats --endpoint "${endpoint}" "${volumeid}:${target_path}"
sleep 2

echo "====: $(date -u) Node publish volume test:"  # Requires routng to amlfs
csc node publish --endpoint "${endpoint}" \
                 --cap MULTI_NODE_MULTI_WRITER,mount,,, \
                 --target-path "${target_path}" \
                 --vol-context "fs-name=lustrefs,mgs-ip-address=${lustre_fs_ip}" \
                 "${volumeid}"
sleep 3

echo "====: $(date -u) Node unpublish volume test:"  # Requires routng to amlfs
csc node unpublish --endpoint "${endpoint}" \
                   --target-path "$target_path" \
                   "$volumeid"

echo "====: $(date -u) Delete volume test:"
csc controller del --endpoint "${endpoint}" "$volumeid"

echo "====: $(date -u) Identity test:"
csc identity plugin-info --endpoint "${endpoint}"

echo "====: $(date -u) Node get info test:"
csc node get-info --endpoint "${endpoint}"

echo "$(date -u) Integration test on aks is completed."
