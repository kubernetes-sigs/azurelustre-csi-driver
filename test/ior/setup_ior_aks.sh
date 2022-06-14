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
# Shell script to setup MPI/IOR cluster
#
set -o errexit
set -o pipefail
set -o nounset

testCaseName=${1:-""}

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

echo "show host_file content"
cat ./host_file

for pod in $pods
do
  echo "Copying host_file to Pod $pod"
  kubectl cp ./host_file $pod:/app/host_file
done

rm --force ./host_file

for pod in $pods
do
  ls ./test-config-files/$testCaseName | while read conf
  do
    kubectl cp ./test-config-files/$conf $pod:/app
    echo "$conf copied to $pod"
  done
done

echo "$(date -u) Finish MPI/IOR cluster setup"
