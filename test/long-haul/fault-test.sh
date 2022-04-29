set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

source ./utils.sh

SleepInSecs="60"
TimeIntervalCheckLogInSecs="10"
NodePodNameKeyword="csi-azurelustre-node"
WorkloadPodNameKeyword="azurelustre-deployment-longhaulsample"

print_debug_on_ERR() {
    print_logs_title "DEBUG START"

    csiDriver=$(kubectl get po -n kube-system | grep azurelustre)
    print_logs_info $csiDriver

    workload=$(kubectl get po | grep $WorkloadPodNameKeyword)
    print_logs_info $workload

    print_logs_info "DEBUG END"
}
trap print_debug_on_ERR ERR

reset_all_on_EXIT() {
    print_logs_title "RESET ALL"
    kubectl delete -f ./sample-workload/deployment_write_print_file.yaml --ignore-not-found
    reset_csi_driver
}
trap reset_all_on_EXIT EXIT

start_workload () {
    kubectl apply -f ./sample-workload/deployment_write_print_file.yaml
}

verify_workload_logs () {
    podName=$1
    lastOutput=$(kubectl logs $podName | tail -n 1 | awk -F, '{print $1}')
    dateOfLastOutput=$(date -d "$lastOutput" +%s)
    dateOfNow=$(date +%s)
    delta=$(($dateOfNow-$dateOfLastOutput))

    threshold=$2
    threshold=${threshold:-10}

    if [[ $delta -lt $threshold ]]; 
    then
        print_logs_info "last output of workload pod is $delta secs before, which is within threshold=$threshold"
    else
        print_logs_error "last output of workload pod is $delta secs before, which is greater than threshold=$threshold"
        failfast
    fi
}

verify_workload () {
    get_running_pod $WorkloadPodNameKeyword podName nodeName
    verify_workload_logs $podName $TimeIntervalCheckLogInSecs

    local return_podName=$1
    local return_nodeName=$2
    eval $return_podName=$podName
    eval $return_nodeName=$nodeName
}

print_logs_title "Reset AKS environment and start sample workload"
reset_csi_driver
start_workload
sleep 5
verify_csi_driver
verify_workload workloadPodName workloadNodeName


print_logs_title "Delete workload pod and verify new workload pod "
kubectl delete po $workloadPodName
sleep $SleepInSecs

verify_workload workloadPodNameNew workloadNodeNameNew
if [[ "$workloadPodName" == "$workloadPodNameNew" ]] ; then
    print_logs_error "workload pod $workloadPodName should be killed and new workload should be started"
    failfast_resetnode
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
    return 1
else
    print_logs_info "Lustre CSI node pod is deleted on $workloadNodeName"
fi


print_logs_title "Verify workload pod on worker node"
verify_workload workloadPodNameNew workloadNodeNameNew
if [[ "$workloadPodName" != "$workloadPodNameNew" || "$workloadNodeName" != "$workloadNodeNameNew" ]] ; then
    print_logs_error "expected workload pod $workloadPodName on $workloadNodeName, actual new workload pod $workloadPodNameNew on $workloadNodeNameNew"
    return 1
fi


print_logs_title "Delete the workload pod on the worker node and verify its state"
kubectl delete po $workloadPodName > /dev/null 2>&1 &
print_logs_info "running 'kubectl delete po' by background task"
sleep $SleepInSecs

podState=$(get_pod_state $workloadPodName $workloadNodeName)
if [[ -z $podState || "$podState" != "Terminating" ]]; then
    print_logs_error "Workload pod $workloadPodName should be in Terminating state on node $workloadNodeName, but its actual state is $podState"
    return 1
else
    print_logs_info "Workload pod $workloadPodName is in Terminating state on node $workloadNodeName"
fi


print_logs_title "Verify the new workload pod in running state"
verify_workload workloadPodNameNew workloadNodeNameNew
if [[ "$workloadPodName" == "$workloadPodNameNew" ]] ; then
    print_logs_error "New workload pod should be started, but still find old running pod $workloadPodName"
    return 1
else
    print_logs_info "new workload pod $workloadPodNameNew started on another node $workloadNodeNameNew"
fi


print_logs_title "Bring Lustre CSI node pod back on the worker node"
kubectl label nodes $workloadNodeName node4faulttest=false --overwrite
sleep $SleepInSecs

podState=$(get_pod_state $NodePodNameKeyword $workloadNodeName)
if  [[ -z "$podState" || "$podState" != "Running" ]]; then
    print_logs_error "Lustre CSI node pod can't be started on $nodeName, state=$podState"
    return 1
else
    print_logs_info "Lustre CSI node pod started on $nodeName again"
fi


print_logs_title "Verify the old workload pod is deleted successfully"
sleep $SleepInSecs

podState=$(get_pod_state $workloadPodName $workloadNodeName)
if [[ ! -z $podState ]]; then
    print_logs_error "Still can find workload pod $workloadPodName in $podState state on node $workloadNodeName, it should be deleted successfully"
    return 1
else
    print_logs_info "workload pod $workloadPodName has been deleted successfully from node $workloadNodeName"
fi