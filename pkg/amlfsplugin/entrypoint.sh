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

apt-get update -y
apt-get install -y libreadline7 libkeyutils1 "$PKG1_1064" "$PKG2_1064"

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
