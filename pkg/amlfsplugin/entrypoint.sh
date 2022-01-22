#!/bin/bash
#
# $1 is the path to the CSI driver.
#
echo $@
apt-get update

if [[ "$(uname -r)" = "5.4.0-1059-azure" ]]; then
    echo "Installing for 1059"
    apt-get install -y libreadline7 kmod fdutils libkeyutils-dev "$PKG1" "$PKG2"
elif [[ "$(uname -r)" = "5.4.0-1063-azure" ]]; then
    echo "Installing for 1063"
    apt-get install -y libreadline7 kmod fdutils libkeyutils-dev "$PKG1_1063" "$PKG2_1063"
else
    echo "Installing for 1064"
    apt-get install -y libreadline7 kmod fdutils libkeyutils-dev "$PKG1_1064" "$PKG2_1064"
fi

modprobe -v ksocklnd
modprobe -v lnet
modprobe -v mgc
modprobe -v lustre
lctl network up
#mkdir -p /host_tmp/lustre
#mount -t lustre $MDS_IP_ADDR@tcp:/lustrefs /host_tmp/lustre
echo "<Lustre CSI driver>"
echo Executing: $1 $2 $3 $4 $5 $6 $7 $8 $9
$1 $2 $3 $4 $5 $6 $7 $8 $9
echo "</Lustre CSI driver>"
