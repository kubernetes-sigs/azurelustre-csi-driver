set -o errexit
set -o pipefail
set -o nounset

export Repo="../../"
export NodePodNameKeyword="csi-azurelustre-node"
export SampleWorkloadKeyword="azurelustre-longhaulsample-deployment"

PoolName=${PoolName:-""}

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

fast_exit () {
    print_debug
    exit 1
}

reset_csi_driver () {
    echo "Reset CSI driver"
    kubectl replace -f $Repo/deploy/csi-azurelustre-controller.yaml
    kubectl replace -f $Repo/deploy/csi-azurelustre-node.yaml

    echo "Reset node label"
    kubectl get nodes --no-headers | grep "$PoolName" | awk '{print $1}' | 
    {
        while read n; 
        do
            kubectl label nodes $n node4faulttest-
        done 
    }

    kubectl wait pod -n kube-system --for=condition=Ready --selector='app in (csi-azurelustre-controller,csi-azurelustre-node)' --timeout=300s
}

get_worker_node_num () {
    workerNodeNum=$(kubectl get nodes | grep "$PoolName" | grep Ready | wc -l)

    echo $workerNodeNum
}

get_pod_by_status () {
    podNameKeyword=${1:-""}
    podStatus=${2:-""}

    pod=$(kubectl get po --all-namespaces -o wide --sort-by=.metadata.creationTimestamp | grep "$PoolName" | grep "$podStatus" | grep "$podNameKeyword" || true)

    if  [ -z "$pod" ] 
    then
        print_logs_error "can't find running pod with keyword=$podNameKeyword"

        pod=$(get_pod $podNameKeyword)

        if  [ -z "$pod" ] 
        then
            podName=$(echo "$pod" | awk '{print $2}')
            podStatus=$(echo "$pod" | awk '{print $4}')
            print_logs_error "find pod $podName in $podStatus state, expect running"
        fi

        fast_exit
    else
         numOfPod=$(echo "$pod" | grep -o -i "$podNameKeyword" | wc -l)

        if [ $numOfPod != 1 ]
        then
            print_logs_error "find $numOfPod running pod with keyword=$podNameKeyword, expect only one"
        fi
    fi

    podName=$(echo $pod | awk '{print $2}')
    nodeName=$(echo $pod | awk '{print $8}')
    actualPodStatus=$(echo $pod | awk '{print $4}')

    print_logs_info "workload pod $podName is running on $nodeName"

    local return_podName=$3
    local return_nodeName=$4
    local return_podStatus=$5

    eval $return_podName=$podName
    eval $return_nodeName=$nodeName
    eval $return_podStatus=$actualPodStatus
}

get_pod_state () {
    podNameKeyword=${1:-""}
    nodeNameKeyword=${2:-""}

    state=$(kubectl get po --all-namespaces -o wide | grep "$PoolName" | grep "$podNameKeyword" | grep "$nodeNameKeyword" | awk '{print $4}' | head -n 1 || true)
    echo "$state"
}

get_pod () {
    podNameKeyword=${1:-""}
    nodeNameKeyword=${2:-""}

    pod=$(kubectl get po --all-namespaces -o wide | grep "$PoolName" | grep "$podNameKeyword" | grep "$nodeNameKeyword" | head -n 1 || true)
    echo "$pod"
}

verify_csi_driver () {
    controllerPodsNum=$(kubectl get po -n kube-system --field-selector=status.phase=Running | grep 'csi-azurelustre-controller' | awk '{print $1}' | wc -l)
    
    if  [ "$controllerPodsNum" != "2" ] 
    then
        print_logs_error "Expected controller pods num 2, actual $controllerPodsNum"
        fast_exit
    else
        print_logs_info "2 controller pods running..."        
    fi

    nodePodsNum=$(kubectl get po -o wide -n kube-system --field-selector=status.phase=Running | grep "$PoolName" | grep "csi-azurelustre-node" | wc -l)
    workerNodeNum=$(get_worker_node_num)

    if  [ "$nodePodsNum" != "$workerNodeNum" ] 
    then
        print_logs_error "Expected node pods num $workerNodeNum, actual $nodePodsNum"
        fast_exit
    else
        print_logs_info "$nodePodsNum node pods running..."        
    fi
}

restart_sample_workload () {
    stop_sample_workload
    start_sample_workload
}

start_sample_workload () {
    stop_sample_workload
    kubectl apply -f ./sample-workload/deployment_write_print_file.yaml --timeout=60s
    kubectl wait pod --for=condition=Ready --selector=app=azurelustre-longhaulsample-deployment --timeout=60s
}

stop_sample_workload () {
    echo "Stop sample workload"
    if [[ ! -z $(kubectl get pvc azurelustre-longhaulsample-pvc --ignore-not-found) ]]; then
        kubectl patch pvc azurelustre-longhaulsample-pvc -p '{"metadata":{"finalizers":null}}'
    fi

    kubectl delete -f ./sample-workload/deployment_write_print_file.yaml --ignore-not-found --timeout=60s --grace-period=0 --force --cascade
    kubectl wait pod --for=delete --selector=app=azurelustre-longhaulsample-deployment --timeout=60s
}

verify_sample_workload_logs () {
    podName=$1
    lastOutput=$(kubectl logs $podName | tail -n 1 | awk -F, '{print $1}')
    dateOfLastOutput=$(date -d "$lastOutput" +%s)
    dateOfNow=$(date +%s)
    delta=$(($dateOfNow-$dateOfLastOutput))

    threshold=${2:-10}

    if [[ $delta -lt $threshold ]]; 
    then
        print_logs_info "last output of workload pod is $delta secs before, which is within threshold=$threshold"
    else
        print_logs_error "last output of workload pod is $delta secs before, which is greater than threshold=$threshold"
        fast_exit
    fi
}

verify_sample_workload_by_pod_status () {
    podStatus=${3:-'Running'}

    get_pod_by_status $SampleWorkloadKeyword $podStatus podName nodeName actualPodStatus

    if [[ "$actualPodStatus" == "Running" ]]; then
        verify_sample_workload_logs $podName $TimeIntervalCheckLogInSecs
    fi
    
    local return_podName=$1
    local return_nodeName=$2
    eval $return_podName=$podName
    eval $return_nodeName=$nodeName
}

print_debug() {
    print_logs_title "Print DEBUG Start"

    bash $Repo/utils/azurelustre_log.sh

    print_logs_title "Print DEBUG End"
}

reset_all() {
    sleep 15
    print_logs_title "RESET ALL Start"

    stop_sample_workload
    reset_csi_driver

    print_logs_title "RESET ALL End"
}