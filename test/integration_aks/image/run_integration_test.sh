#!/bin/bash

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

readonly volname="citest-$(date +%s)"
readonly volsize="2147483648"
readonly endpoint="unix:///csi/csi.sock"
readonly target_path="/tmp/target_path"
readonly lustre_fs_name=$1
readonly lustre_fs_ip=$2
readonly lustre_client_version="2.14.0"

mkdir -p $target_path

echo "$(date -u) Installing Lustre kmod git and cert"
apt-get update
apt-get install -y --no-install-recommends kmod wget git ca-certificates lsb-release gpg curl
update-ca-certificates

osReleaseCodeName=$(lsb_release -cs)
kernelVersion=$(uname -r)
echo "$(date -u) OS release code name is ${osReleaseCodeName}, kernel version is ${kernelVersion}"

echo "$(date -u) Installing Lustre client packages."

curl -sL https://packages.microsoft.com/keys/microsoft.asc | gpg --dearmor | tee /etc/apt/trusted.gpg.d/microsoft.gpg > /dev/null
echo "deb [arch=amd64] https://packages.microsoft.com/repos/amlfs/ ${osReleaseCodeName} main" | tee /etc/apt/sources.list.d/amlfs.list
apt-get update
apt install -y --no-install-recommends lustre-client-modules-${kernelVersion} lustre-client-utils

echo "$(date -u) Installed Lustre client packages."

echo "$(date -u) Enabling Lustre client kernel modules."
modprobe -v ksocklnd
modprobe -v lnet
modprobe -v mgc
modprobe -v lustre

# For some reason, this is a false positive before we restart the container
# The volume mount succeeds later even this returns a failure
# We need to revisit this after moving the script to run on AKS node
lctl network up || true
echo "$(date -u) Enabled Lustre client kernel modules."

echo "$(date -u) Entering Lustre CSI driver"

echo "$(date -u) install csc"
GO111MODULE=off go get github.com/rexray/gocsi/csc

mkdir /csi
echo "$(date -u) Exiting Lustre CSI driver"
nohup 2>&1 ./azurelustreplugin --v=5 \
              --endpoint=$endpoint \
              --nodeid=integrationtestnode \
              --metrics-address=0.0.0.0:29635 >csi.log &

sleep 5

echo "====: $(date -u) Exiting integration test"
export X_CSI_DEBUG=true
echo "====: $(date -u) Create volume test:"
value="$(csc controller new --endpoint "$endpoint" \
                            --cap MULTI_NODE_MULTI_WRITER,mount,,, \
                            "$volname" \
                            --req-bytes "$volsize" \
                            --params fs-name=$lustre_fs_name,mds-ip-address=$lustre_fs_ip)"
sleep 5

volumeid="$(echo "$value" | awk '{print $1}' | sed 's/"//g')"
echo "====: $(date -u) Volume ID is $volumeid"

echo "====: $(date -u) Validate volume capabilities test:"
csc controller validate-volume-capabilities --endpoint "$endpoint" \
                                            --cap MULTI_NODE_MULTI_WRITER,mount,,, \
                                            "$volumeid"

echo "====: $(date -u) Node publish volume test:"
csc node publish --endpoint "$endpoint" \
                 --cap MULTI_NODE_MULTI_WRITER,mount,,, \
                 --target-path "$target_path" \
                 --vol-context "fs-name=$lustre_fs_name,mds-ip-address=$lustre_fs_ip" \
                 "$volumeid"
sleep 3

echo "====: $(date -u) Node unpublish volume test:"
csc node unpublish --endpoint "$endpoint" \
                   --target-path "$target_path" \
                   "$volumeid"

echo "====: $(date -u) Delete volume test:"
csc controller del --endpoint "$endpoint" "$volumeid"

echo "====: $(date -u) Identity test:"
csc identity plugin-info --endpoint "$endpoint"

echo "====: $(date -u) Node get info test:"
csc node get-info --endpoint "$endpoint"

echo "$(date -u) Integration test on aks is completed."
