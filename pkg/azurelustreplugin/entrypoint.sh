#!/bin/bash

#
# Shell script to install Lustre client kernel modules and launch CSI driver
#   $1 is the path to the CSI driver.
#

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

installClientPackages=${AZURELUSTRE_CSI_INSTALL_LUSTRE_CLIENT:-yes}
echo "installClientPackages: ${installClientPackages}"

echo "$(date -u) Command line arguments: $@"

if [[ "${installClientPackages}" == "yes" ]]; then

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

fi

echo "$(date -u) Entering Lustre CSI driver"

echo Executing: $1 ${2-} ${3-} ${4-} ${5-} ${6-} ${7-} ${8-} ${9-}
$1 ${2-} ${3-} ${4-} ${5-} ${6-} ${7-} ${8-} ${9-}

echo "$(date -u) Exiting Lustre CSI driver"
