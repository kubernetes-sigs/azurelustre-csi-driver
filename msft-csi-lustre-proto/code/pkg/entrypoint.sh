#!/bin/bash
#
# $1 is the path to the CSI driver.
#
echo $@
apt-get update

kv=$(uname -r)
if [[ "$kv" == "5.4.0-1059-azure" ]]; then
    apt-get install -y libreadline7 kmod fdutils "$PKG1" "$PKG2"
else
    apt-get install -y libreadline7 kmod fdutils "$PKG3" "$PKG4"
fi
modprobe -v ksocklnd
modprobe -v lnet
modprobe -v mgc
modprobe -v lustre
lctl network up
#mkdir -p /host_tmp/lustre
#mount -t lustre $MDS_IP_ADDR@tcp:/lustrefs /host_tmp/lustre
echo "<Lustre CSI driver>"
$1 $2 $3 $4
echo "</Lustre CSI driver>"/
