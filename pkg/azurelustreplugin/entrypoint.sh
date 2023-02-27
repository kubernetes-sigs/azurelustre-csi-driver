#!/bin/bash

# Copyright 2022 The Kubernetes Authors.
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

requiredLustreVersion=${LUSTRE_VERSION:-"2.15.1"}
echo "requiredLustreVersion: ${requiredLustreVersion}"

pkgVersion="${requiredLustreVersion}-24-gbaa21ca"
echo "pkgVersion: ${pkgVersion}"

pkgName="amlfs-lustre-client-${pkgVersion}"
echo "pkgName: ${pkgName}"

if [[ ! -z $(grep -R 'bionic' /etc/os-release) ]]; then
  osReleaseCodeName="bionic"
elif [[ ! -z $(grep -R 'jammy' /etc/os-release) ]]; then
#   cat << EOF | tee /etc/apt/sources.list.d/jammy.list
# deb http://azure.archive.ubuntu.com/ubuntu/ jammy main restricted
# deb http://azure.archive.ubuntu.com/ubuntu/ jammy-updates main restricted
# deb http://azure.archive.ubuntu.com/ubuntu/ jammy universe
# deb http://azure.archive.ubuntu.com/ubuntu/ jammy-updates universe
# deb http://azure.archive.ubuntu.com/ubuntu/ jammy multiverse
# deb http://azure.archive.ubuntu.com/ubuntu/ jammy-updates multiverse
# deb http://azure.archive.ubuntu.com/ubuntu/ jammy-backports main restricted universe multiverse
# deb http://azure.archive.ubuntu.com/ubuntu/ jammy-security main restricted
# deb http://azure.archive.ubuntu.com/ubuntu/ jammy-security universe
# deb http://azure.archive.ubuntu.com/ubuntu/ jammy-security multiverse
# EOF
# 
  osReleaseCodeName="jammy"
else
  echo "Unsupported Linux distro"
  exit 1
fi

echo "$(date -u) Command line arguments: $@"

if [[ "${installClientPackages}" == "yes" ]]; then
  kernelVersion=$(uname -r)

  echo "$(date -u) Installing Lustre client packages for OS=${osReleaseCodeName}, kernel=${kernelVersion} "

  if [ ! -f /etc/apt/sources.list.d/amlfs.list ]; then
    curl -sL https://packages.microsoft.com/keys/microsoft.asc | gpg --dearmor | tee /etc/apt/trusted.gpg.d/microsoft.gpg > /dev/null
    echo "deb [arch=amd64] https://packages.microsoft.com/repos/amlfs-${osReleaseCodeName}/ ${osReleaseCodeName} main" | tee /etc/apt/sources.list.d/amlfs.list
    apt-get update
  fi
  
  echo "$(date -u) Installing Lustre client modules: ${pkgName}=${kernelVersion}"

  # grub issue
  # https://stackoverflow.com/questions/40748363/virtual-machine-apt-get-grub-issue/40751712
  DEBIAN_FRONTEND=noninteractive apt install -y --no-install-recommends -o DPkg::options::="--force-confdef" -o DPkg::options::="--force-confold" \
    ${pkgName}=${kernelVersion}

  echo "$(date -u) Installed Lustre client packages."

  init_lnet="true"
  
  if lsmod | grep "^lnet"; then
    if lnetctl net show --net tcp | grep interfaces; then
      echo "$(date -u) LNet is loaded skip the load."
      init_lnet="false"
    fi    
  fi

  if [[ "${init_lnet}" == "true" ]]; then
    echo "$(date -u) Loading the LNet."
    modprobe -v lnet
    lnetctl lnet configure

    echo "$(date -u) Determining the default network interface."
    # perl will be installed as dependency by luster client
    echo "$(date -u) Route table is:"
    ip route list
    default_interface=$(ip route list | perl -n -e'/default via [0-9.]+ dev ([0-9a-zA-Z]+) / && print $1')
    echo "$(date -u) Default network interface is ${default_interface}"

    if [[ "${default_interface}" == "" ]]; then
      echo "$(date -u) Cannot determine the default network interface"
      exit 1
    fi

    if lnetctl net show | grep "net type: tcp"; then
    # There may be a default configuration with no interface.
    # This is configured by an old version CSI.
      lnetctl net del --net tcp
    fi

    lnetctl net add --net tcp --if "${default_interface}"

    echo "$(date -u) Adding the udev script."
    test -e /etc/lustre || mkdir /etc/lustre
    touch /etc/lustre/.lock
    test -e /etc/lustre/fix-lnet.sh && rm -f /etc/lustre/fix-lnet.sh
    sed -i "s/{default_interface}/${default_interface}/g;" ./fix-lnet.sh
    cp ./fix-lnet.sh /etc/lustre

    test -e /etc/udev/rules.d/73-netadd.rules && rm -f /etc/udev/rules.d/73-netadd.rules
    test -e /etc/udev/rules.d/74-netremove.rules && rm -f /etc/udev/rules.d/74-netremove.rules
    echo 'SUBSYSTEM=="net", ACTION=="add", RUN+="/etc/lustre/fix-lnet.sh"' | tee /etc/udev/rules.d/73-netadd.rules
    echo 'SUBSYSTEM=="net", ACTION=="remove", RUN+="/etc/lustre/fix-lnet.sh"' | tee /etc/udev/rules.d/74-netremove.rules

    echo "$(date -u) Reloading udevadm"
    udevadm control --reload
    echo "$(date -u) Done"
  fi

  echo "$(date -u) Enabling Lustre client kernel modules."
  modprobe -v mgc
  modprobe -v lustre

  echo "$(date -u) Enabled Lustre client kernel modules."

fi

echo "$(date -u) Entering Lustre CSI driver"

echo Executing: $1 ${2-} ${3-} ${4-} ${5-} ${6-} ${7-} ${8-} ${9-}
$1 ${2-} ${3-} ${4-} ${5-} ${6-} ${7-} ${8-} ${9-}

echo "$(date -u) Exiting Lustre CSI driver"
