#!/bin/bash

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

rg=jusjin-test-temp
location=eastus2
vmName=jusjin-test-vm
vmUsername=azureuser
imageUrn=Canonical:UbuntuServer:18.04-LTS:18.04.202111300
vmDnsName=${vmName}.${location}.cloudapp.azure.com

# az group create --name $rg --location $location

# az vm create                                          \
#   --resource-group $rg                                \
#   --name $vmName                                      \
#   --admin-username $vmUsername                        \
#   --authentication-type ssh                           \
#   --generate-ssh-keys                                 \
#   --public-ip-address-dns-name $vmName                \
#   --image $imageUrn                                   \
#   --size standard_d32s_v3							  	\

# kernelVersion=$(ssh -o "StrictHostKeyChecking=no" ${vmUsername}@${vmDnsName} "uname -r")
# echo $kernelVersion


# ssh -o "StrictHostKeyChecking=no" -t ${vmUsername}@${vmDnsName} <<'ENDSSH'
# sudo su -
# apt-get update
# apt install -y linux-image-$(uname -r) libtool m4 autotools-dev automake libelf-dev build-essential debhelper devscripts fakeroot kernel-wedge libudev-dev libpci-dev texinfo xmlto libelf-dev python-dev liblzma-dev libaudit-dev dh-systemd libyaml-dev module-assistant libreadline-dev dpatch libsnmp-dev quilt python3.7 python3.7-dev python3.7-distutils pkg-config libselinux1-dev mpi-default-dev libiberty-dev libpython3.7-dev libpython3-dev swig flex bison
# git clone git://git.whamcloud.com/fs/lustre-release.git
# cd lustre-release
# git checkout 2.14.0
# git reset --hard && git clean -dfx && sh autogen.sh
# ./configure --disable-server
# make debs -j 28
# ENDSSH

# ssh -o "StrictHostKeyChecking=no" ${vmUsername}@${vmDnsName} #"sudo apt-get update >> log.txt 2>&1"
# ssh -o StrictHostKeyChecking=no azureuser@jusjin-test-vm.eastus2.cloudapp.azure.com

# sudo su -
# 
# apt-get update
# 
# apt install -y linux-image-$(uname -r) libtool m4 autotools-dev automake libelf-dev build-essential debhelper devscripts fakeroot kernel-wedge libudev-dev libpci-dev texinfo xmlto libelf-dev python-dev liblzma-dev libaudit-dev dh-systemd libyaml-dev module-assistant libreadline-dev dpatch libsnmp-dev quilt python3.7 python3.7-dev python3.7-distutils pkg-config libselinux1-dev mpi-default-dev libiberty-dev libpython3.7-dev libpython3-dev swig flex bison
# 
# git clone git://git.whamcloud.com/fs/lustre-release.git
# 
# cd lustre-release
# 
# git checkout 2.14.0
# 
# git reset --hard && git clean -dfx && sh autogen.sh
# 
# ./configure --disable-server
# 
# make debs -j 28

# wget https://aka.ms/downloadazcopy-v10-linux
# 
# tar -zxf downloadazcopy-v10-linux
# 
# ./azcopy copy "../lustre-client-*" "https://jusjincsi.blob.core.windows.net/packages?sp=rw&st=2021-12-21T09:11:11Z&se=2021-12-21T17:11:11Z&spr=https&sv=2020-08-04&sr=c&sig=ov53w2aflsJz7glzLW4ZaUGb93RQ0lRAfX%2BSigGgeSc%3D"

# az group delete --name $rg --yes