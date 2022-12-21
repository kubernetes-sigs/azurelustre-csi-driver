#!/bin/sh

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

if [[ ! -d "/host" ]]; then
  echo "Need to mount host root to /host" >&2
  exit 1
fi

echo "Installing csi program to /host/azurelustrecsi"

if [[ -d "/host/usr/local/azurelustrecsi" ]]; then
  rm -rf /host/usr/local/azurelustrecsi
fi

mkdir /host/usr/local/azurelustrecsi
cp /app/* /host/usr/local/azurelustrecsi/

echo "Changing root to /host"

chroot /host /bin/bash << EOF
cd /usr/local/azurelustrecsi
./csientrypoint.sh
EOF
