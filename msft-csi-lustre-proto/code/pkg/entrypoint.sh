#!/bin/bash
#
# $1 is the path to the CSI driver.
#
echo $@
apt-get update
apt-get install -y libreadline7 kmod fdutils "$PKG1" "$PKG2"

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
echo "</Lustre CSI driver>"/
