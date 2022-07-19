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
# Shell script to run MPI/IOR cluster
#
set -o errexit
set -o pipefail
set -o nounset

repo="$(git rev-parse --show-toplevel)/test/ior"

skipSetup=${1:-"false"}

if [[ "$skipSetup" == "false" ]]; 
then 
    echo "Removing IOR pods"
    kubectl delete -f $repo/pod.yaml --ignore-not-found
    sleep 15

    echo "Creating IOR pods"
    kubectl apply -f $repo/pod.yaml
    sleep 15

    echo "Setup IOR pods"
    $repo/setup_ior_aks.sh
    sleep 15
fi

pod1=$(kubectl get po --no-headers | grep "ior" | awk '{print $1}' | head -n 1)
pod2=$(kubectl get po --no-headers | grep "ior" | awk '{print $1}' | tail -n 1)

resultsDirectory="results$(date +%s)"
mkdir $repo/$resultsDirectory

echo "$(date -u) Starting IOR execution, find test results in directory $repo/$resultsDirectory"

clientPerNode=64
echo "$(date -u) executing IOR cases for $clientPerNode client per node"

sleep 60
testcase="bw_n_to_n_rnd_buffered"
echo "$(date -u) executing $testcase"
kubectl exec -it $pod1 -- mpirun --hostfile /app/host_file --map-by node -np $clientPerNode ior -o /azurelustre/test_file -f /app/$testcase 2>&1 >> "$repo/$resultsDirectory/${testcase}_${clientPerNode}"

sleep 60
testcase="bw_n_to_n_rnd_direct"
echo "$(date -u) executing $testcase"
kubectl exec -it $pod2 -- mpirun --hostfile /app/host_file --map-by node -np $clientPerNode ior -o /azurelustre/test_file -f /app/$testcase 2>&1 >> "$repo/$resultsDirectory/${testcase}_${clientPerNode}"

sleep 60
testcase="bw_n_to_n_seq_buffered"
echo "$(date -u) executing $testcase"
kubectl exec -it $pod1 -- mpirun --hostfile /app/host_file --map-by node -np $clientPerNode ior -o /azurelustre/test_file -f /app/$testcase 2>&1 >> "$repo/$resultsDirectory/${testcase}_${clientPerNode}"

sleep 60
testcase="bw_n_to_n_seq_direct"
echo "$(date -u) executing $testcase"
kubectl exec -it $pod2 -- mpirun --hostfile /app/host_file --map-by node -np $clientPerNode ior -o /azurelustre/test_file -f /app/$testcase 2>&1 >> "$repo/$resultsDirectory/${testcase}_${clientPerNode}"

sleep 60
testcase="iops_n_to_1_rnd_buffered"
echo "$(date -u) executing $testcase"
kubectl exec -it $pod1 -- mpirun --hostfile /app/host_file --map-by node -np $clientPerNode ior -o /azurelustre/test_file -f /app/$testcase 2>&1 >> "$repo/$resultsDirectory/${testcase}_${clientPerNode}"

sleep 60
testcase="iops_n_to_1_rnd_direct"
echo "$(date -u) executing $testcase"
kubectl exec -it $pod2 -- mpirun --hostfile /app/host_file --map-by node -np $clientPerNode ior -o /azurelustre/test_file -f /app/$testcase 2>&1 >> "$repo/$resultsDirectory/${testcase}_${clientPerNode}"

sleep 60
testcase="iops_n_to_n_rnd_buffered"
echo "$(date -u) executing $testcase"
kubectl exec -it $pod1 -- mpirun --hostfile /app/host_file --map-by node -np $clientPerNode ior -o /azurelustre/test_file -f /app/$testcase 2>&1 >> "$repo/$resultsDirectory/${testcase}_${clientPerNode}"

sleep 60
testcase="iops_n_to_n_rnd_direct"
echo "$(date -u) executing $testcase"
kubectl exec -it $pod2 -- mpirun --hostfile /app/host_file --map-by node -np $clientPerNode ior -o /azurelustre/test_file -f /app/$testcase 2>&1 >> "$repo/$resultsDirectory/${testcase}_${clientPerNode}"

echo "$(date -u) Finished IOR execution"
