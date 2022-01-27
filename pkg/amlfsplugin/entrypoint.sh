#!/bin/bash

#
# Shell script to install Lustre client kernel modules and launch CSI driver
#   $1 is the path to the CSI driver.
#

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

echo Command line arguments: $@

urlPrefix="https://amlfscsiinfrasa.blob.core.windows.net/lustre-client-module/canonical/ubuntuserver/18.04-lts"
kernelVersion=$(uname -r)

wget "${urlPrefix}/${kernelVersion}/lustre-client-utils_2.14.0_amd64.deb"
wget "${urlPrefix}/${kernelVersion}/lustre-client-modules_2.14.0_amd64.deb"

apt-get update -y
apt-get install -y "./lustre-client-utils_2.14.0_amd64.deb" "./lustre-client-modules_2.14.0_amd64.deb"

apt-get autoremove -y wget

rm --force ./lustre-client-utils_2.14.0_amd64.deb
rm --force ./lustre-client-modules_2.14.0_amd64.deb

modprobe -v ksocklnd
modprobe -v lnet
modprobe -v mgc
modprobe -v lustre

# For some reason, this is a false positive before we restart the container
# The volume mount succeeds later even this returns a failure
# We need to revisit this after moving the script to run on AKS node
lctl network up || true

echo "<Lustre CSI driver>"
echo Executing: $1 ${2-} ${3-} ${4-} ${5-} ${6-} ${7-} ${8-} ${9-}
$1 ${2-} ${3-} ${4-} ${5-} ${6-} ${7-} ${8-} ${9-}
echo "</Lustre CSI driver>"
