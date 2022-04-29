Repo="../../"

print_logs_case () {
    echo -e "\n$(date '+%Y-%m-%d %H:%M:%S') INFO: =================  $1 ================= \n"
}

print_logs_title () {
    echo -e "\n$(date '+%Y-%m-%d %H:%M:%S') INFO: -----------------  $1 ----------------- \n"
}

print_logs_info () {
    echo -e "$(date '+%Y-%m-%d %H:%M:%S') INFO: $1 \n"
}

print_logs_error () {
    echo -e "$(date '+%Y-%m-%#d %H:%M:%S') ERROR: $1 \n"
}

reset_csi_driver () {
    kubectl replace -f $Repo/deploy/csi-azurelustre-node.yaml

    kubectl get nodes --no-headers | awk '{print $1}' | 
    {
        while read n; 
        do
            kubectl label nodes $n node4faulttest-
        done 
    }    
}

get_worker_node_num () {
    workerNodeNum=$(kubectl get nodes | grep Ready | wc -l)

    echo $workerNodeNum
}

get_running_pod () {    
    podKeyword=$1
    podKeyword=${podKeyword:-""}

    pod=$(kubectl get po --all-namespaces -o wide --sort-by=.metadata.creationTimestamp | grep Running | grep "$podKeyword" | head -n 1 || true)

    if  [ -z "$pod" ] 
    then
        print_logs_error "can't find running pod with keyword=$podKeyword"
        return 1
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

    state=$(kubectl get po --all-namespaces -o wide | grep "$podNameKeyword" | grep "$nodeNameKeyword" | awk '{print $4}')
    echo "$state"
}

verify_csi_driver () {
    controllerPodsNum=$(kubectl get pods -n kube-system --field-selector=status.phase=Running | grep 'csi-azurelustre-controller' | awk '{print $1}' | wc -l)
    
    if  [ "$controllerPodsNum" != "2" ] 
    then
        print_logs_error "Expected controller pods num 2, actual $controllerPodsNum"
        return 1
    else
        print_logs_info "2 controller pods running..."        
    fi

    nodePodsNum=$(kubectl get pods -n kube-system --field-selector=status.phase=Running | grep "csi-azurelustre-node" | awk '{print $1}' | wc -l)
    workerNodeNum=$(get_worker_node_num)

    if  [ "$nodePodsNum" != "$workerNodeNum" ] 
    then
        print_logs_error "Expected node pods num $workerNodeNum, actual $nodePodsNum"
        return 1
    else
        print_logs_info "$nodePodsNum node pods running..."        
    fi
}