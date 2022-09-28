# Copyright 2020 The Kubernetes Authors.
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

#!/bin/bash

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

source ./utils.sh

SleepInSecs="100"

trap print_debug ERR

print_logs_title "Reset AKS environment and start sample workload"
reset_all
start_sample_workload
verify_csi_driver
verify_sample_workload_by_pod_status workloadPodName workloadNodeName


print_logs_title "Delete workload pod and verify new workload pod "
kubectl delete po $workloadPodName
sleep $SleepInSecs

verify_sample_workload_by_pod_status workloadPodNameNew workloadNodeNameNew
if [[ "$workloadPodName" == "$workloadPodNameNew" ]] ; then
    print_logs_error "workload pod $workloadPodName should be killed and new workload should be started"
    print_debug_on_ERR
    fast_exit
fi

workloadPodName=$workloadPodNameNew
workloadNodeName=$workloadNodeNameNew


print_logs_title "Add label for worker nodes"
kubectl get nodes --no-headers | awk '{print $1}' | 
{
    while read n;
    do
        if  [[ $n == "$workloadNodeName" ]]; then
            print_logs_info "set label node4faulttest=TRUE for $n"
            kubectl label nodes $n node4faulttest=true --overwrite
        else
            print_logs_info "set label node4faulttest=FALSE for $n"
            kubectl label nodes $n node4faulttest=false --overwrite
        fi
    done
}


print_logs_title "Remove Lustre CSI node pod"
kubectl patch daemonset $NodePodNameKeyword -n kube-system -p '{"spec": {"template": {"spec": {"nodeSelector": {"node4faulttest": "false"}}}}}'
sleep $SleepInSecs


print_logs_title "Verify Lustre CSI node pod removed from the worker node"
podState=$(get_pod_state $NodePodNameKeyword $workloadNodeName)

if  [[ ! -z "$podState" ]]; then
    print_logs_error "Lustre CSI node pod can't be deleted on $workloadNodeName, state=$podState"
    fast_exit
else
    print_logs_info "Lustre CSI node pod is deleted on $workloadNodeName"
fi


print_logs_title "Verify workload pod on the same worker node is still working"
verify_sample_workload_by_pod_status workloadPodNameNew workloadNodeNameNew
if [[ "$workloadPodName" != "$workloadPodNameNew" || "$workloadNodeName" != "$workloadNodeNameNew" ]] ; then
    print_logs_error "expected workload pod $workloadPodName on $workloadNodeName, actual new workload pod $workloadPodNameNew on $workloadNodeNameNew"
    fast_exit
fi


print_logs_title "Delete the workload pod on the worker node and verify its state"
kubectl delete po $workloadPodName > /dev/null 2>&1 &
print_logs_info "running 'kubectl delete po' by background task"
sleep $SleepInSecs

podState=$(get_pod_state $workloadPodName $workloadNodeName)
if [[ -z $podState || "$podState" != "Terminating" ]]; then
    print_logs_error "Workload pod $workloadPodName should be in Terminating state on node $workloadNodeName, but its actual state is $podState"
    print_debug_on_ERR
    fast_exit
else
    print_logs_info "Workload pod $workloadPodName is in Terminating state on node $workloadNodeName"
fi


print_logs_title "Verify the new workload pod in Running state on other nodes or ContainerCreating state on the same node"
verify_sample_workload_by_pod_status workloadPodNameNew workloadNodeNameNew "Running\|ContainerCreating"
if [[ "$workloadPodName" == "$workloadPodNameNew" ]] ; then
    print_logs_error "New workload pod should be started, but still find old running pod $workloadPodName"
    print_debug_on_ERR
    fast_exit
else
    print_logs_info "new workload pod $workloadPodNameNew started on another node $workloadNodeNameNew"
fi


print_logs_title "Bring Lustre CSI node pod back on the worker node"
kubectl label nodes $workloadNodeName node4faulttest=false --overwrite
sleep $SleepInSecs

podState=$(get_pod_state $NodePodNameKeyword $workloadNodeName)
if  [[ -z "$podState" || "$podState" != "Running" ]]; then
    print_logs_error "Lustre CSI node pod can't be started on $nodeName, state=$podState"
    print_debug_on_ERR
    fast_exit
else
    print_logs_info "Lustre CSI node pod started on $nodeName again"
fi


print_logs_title "Verify the old workload pod is deleted successfully"
sleep $SleepInSecs

podState=$(get_pod_state $workloadPodName $workloadNodeName)
if [[ ! -z $podState ]]; then
    print_logs_error "Still can find workload pod $workloadPodName in $podState state on node $workloadNodeName, it should be deleted successfully"
    print_debug_on_ERR
    fast_exit
else
    print_logs_info "workload pod $workloadPodName has been deleted successfully from node $workloadNodeName"
fi

reset_all