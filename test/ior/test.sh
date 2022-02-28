#!/bin/bash

#
# Shell script to run MPI/IOR test
#

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

echo "$(date -u) Start tests on MPI/IOR cluster"

pod=$(kubectl get pods | grep ior | awk '{print $1}' | head -n 1)

kubectl exec $pod -- mpirun --hostfile /app/host_file --map-by node     \
  ior -a POSIX -z -w -r -i 1 -m -d 1 -o /amlfs/test_file -t 1m -b 1g -A 123456 -F -C --posix.odirect # -O summaryFormat=JSON -O summaryFile=/amlfs/out_file

kubectl exec $pod -- mpirun --hostfile /app/host_file --map-by node     \
  mdtest -n 1000 -d /amlfs/ -i 1 -u

echo "$(date -u) Finish tests on MPI/IOR cluster"
