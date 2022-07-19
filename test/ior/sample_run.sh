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
# Shell script to run MPI/IOR test
#

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

echo "$(date -u) Start tests on MPI/IOR cluster"

pod=$(kubectl get pods | grep ior | awk '{print $1}' | head -n 1)

kubectl exec $pod -- mpirun --hostfile /app/host_file --map-by node     \
  ior -a POSIX -z -w -r -i 1 -m -d 1 -o /azurelustre/test_file -t 1m -b 1g -A 123456 -F -C --posix.odirect # -O summaryFormat=JSON -O summaryFile=/azurelustre/out_file

kubectl exec $pod -- mpirun --hostfile /app/host_file --map-by node     \
  mdtest -n 1000 -d /azurelustre/ -i 1 -u

echo "$(date -u) Finish tests on MPI/IOR cluster"
