#!/bin/bash

#
# Shell script to setup MPI/IOR cluster
#

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

echo "$(date -u) Start to setup MPI/IOR cluster"

pods=$(kubectl get pods | grep ior | awk '{print $1}')

ips=$(kubectl get pods -o wide | grep ior | awk '{print $6}')

rm --force ./host_file

for pod in $pods
do

  for ip in $ips
  do
    echo "Pod $pod sshing to ip $ip"
    kubectl exec $pod -- ssh -o StrictHostKeyChecking=no $ip rm --force /app/host_file
  done

done

for ip in $ips
do
  echo "Adding ip $ip to host_file"
  echo $ip >> ./host_file
done

cat ./host_file | sort -u | tee ./host_file

for pod in $pods
do
  echo "Copying host_file to Pod $pod"
  kubectl cp ./host_file $pod:/app/host_file
done

rm --force ./host_file

echo "$(date -u) Finish MPI/IOR cluster setup"
