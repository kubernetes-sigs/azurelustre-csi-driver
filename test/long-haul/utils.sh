set -o errexit
set -o pipefail
set -o nounset

export Repo="../../"
export NodePodNameKeyword="csi-azurelustre-node"
export SampleWorkloadKeyword="azurelustre-deployment-longhaulsample"

TimeIntervalCheckLogInSecs="10"

print_logs_case () {
    echo -e "\n$(date '+%Y-%m-%d %H:%M:%S') INFO: =================  $1 ================="
}

print_logs_title () {
    echo -e "\n$(date '+%Y-%m-%d %H:%M:%S') INFO: -----------------  $1 -----------------"
}

print_logs_info () {
    echo -e "$(date '+%Y-%m-%d %H:%M:%S') INFO: $1"
}

print_logs_error () {
    echo -e "$(date '+%Y-%m-%#d %H:%M:%S') ERROR: $1"
}

signal_err () {
    return 1
}

reset_csi_driver () {
    kubectl replace -f $Repo/deploy/csi-azurelustre-node.yaml

    kubectl get nodes --no-headers | grep "$PoolName" | awk '{print $1}' | 
    {
        while read n; 
        do
            kubectl label nodes $n node4faulttest-
        done 
    }    
}

get_worker_node_num () {
    workerNodeNum=$(kubectl get nodes | grep "$PoolName" | grep Ready | wc -l)

    echo $workerNodeNum
}

get_running_pod () {    
    podNameKeyword=$1
    podNameKeyword=${podNameKeyword:-""}

    pod=$(kubectl get po --all-namespaces -o wide --sort-by=.metadata.creationTimestamp | grep "$PoolName" | grep Running | grep "$podNameKeyword" | head -n 1 || true)

    if  [ -z "$pod" ] 
    then
        print_logs_error "can't find running pod with keyword=$podNameKeyword"

        podState=$(get_pod_state $podNameKeyword)

        if  [ -z "$pod" ] 
        then
            print_logs_error "find pod with keyword=$podNameKeyword in $podState state, expect running"
        fi

        signal_err
    fi

    podName=$(echo $pod | awk '{print $2}')
    nodeName=$(echo $pod | awk '{print $8}')

    print_logs_info "workload pod $podName is running on $nodeName"

    local return_podName=$2
    local return_nodeName=$3

    eval $return_podName=$podName
    eval $return_nodeName=$nodeName
}

get_pod_state () {
    podNameKeyword=$1
    podNameKeyword=${podNameKeyword:-""}

    nodeNameKeyword=$2
    nodeNameKeyword=${nodeNameKeyword:-""}

    state=$(kubectl get po --all-namespaces -o wide | grep "$PoolName" | grep "$podNameKeyword" | grep "$nodeNameKeyword" | awk '{print $4}' | head -n 1 || true)
    echo "$state"
}

verify_csi_driver () {
    controllerPodsNum=$(kubectl get po -n kube-system --field-selector=status.phase=Running | grep 'csi-azurelustre-controller' | awk '{print $1}' | wc -l)
    
    if  [ "$controllerPodsNum" != "2" ] 
    then
        print_logs_error "Expected controller pods num 2, actual $controllerPodsNum"
        signal_err
    else
        print_logs_info "2 controller pods running..."        
    fi

    nodePodsNum=$(kubectl get po -o wide -n kube-system --field-selector=status.phase=Running | grep "$PoolName" | grep "csi-azurelustre-node" | wc -l)
    workerNodeNum=$(get_worker_node_num)

    if  [ "$nodePodsNum" != "$workerNodeNum" ] 
    then
        print_logs_error "Expected node pods num $workerNodeNum, actual $nodePodsNum"
        signal_err
    else
        print_logs_info "$nodePodsNum node pods running..."        
    fi
}

start_sample_workload () {
    kubectl apply -f ./sample-workload/deployment_write_print_file.yaml
}

stop_sample_workload () {
    kubectl delete -f ./sample-workload/deployment_write_print_file.yaml --ignore-not-found
}

verify_sample_workload_logs () {
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
        signal_err
    fi
}

verify_sample_workload () {
    get_running_pod $SampleWorkloadKeyword podName nodeName
    verify_sample_workload_logs $podName $TimeIntervalCheckLogInSecs

    local return_podName=$1
    local return_nodeName=$2
    eval $return_podName=$podName
    eval $return_nodeName=$nodeName
}

print_debug_on_ERR() {
    print_logs_title "Print DEBUG Start"

    csiDriver=$(kubectl get po -n kube-system -o wide | grep "$PoolName"  | grep azurelustre)
    echo $csiDriver

    workload=$(kubectl get po -o wide | grep "$PoolName" | grep $SampleWorkloadKeyword)
    echo $workload

    print_logs_title "Print DEBUG End"
}

reset_all_on_EXIT() {
    sleep 30

    print_logs_title "RESET ALL Start"

    stop_sample_workload
    reset_csi_driver

    print_logs_title "RESET ALL End"
}