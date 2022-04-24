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

urlPrefix="https://azurelustrecsiinfrasa.blob.core.windows.net/azurelustre-client-packages/bionic"
kernelVersion=$(uname -r)

if [[ "${installClientPackages}" == "yes" ]]; then

  echo "$(date -u) Downloading Lustre client packages."

  # For some reason, wget doesn't trust the cert of azure blob url today
  # Use --no-check-certificate as a workaround for now before we onboard to packages.microsoft.com
  wget --no-check-certificate "${urlPrefix}/${kernelVersion}/lustre-client-utils_2.14.0_amd64.deb"
  wget --no-check-certificate "${urlPrefix}/${kernelVersion}/lustre-client-modules_2.14.0_amd64.deb"

  echo "$(date -u) Downloaded Lustre client packages."

  echo "$(date -u) Installing Lustre client packages."

  apt-get update
  apt-get install -y --no-install-recommends "./lustre-client-utils_2.14.0_amd64.deb" "./lustre-client-modules_2.14.0_amd64.deb"

  apt-get autoremove -y wget

  rm --force ./lustre-client-utils_2.14.0_amd64.deb
  rm --force ./lustre-client-modules_2.14.0_amd64.deb

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
