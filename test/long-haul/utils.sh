Repo="../../"

print_logs_case () {
    printf "\n$(date '+%Y-%m-%d %H:%M:%S') INFO: =================  $1 ================= \n"
}

print_logs_title () {
    printf "\n$(date '+%Y-%m-%d %H:%M:%S') INFO: -----------------  $1 ----------------- \n"
}

print_logs_info () {
    printf "$(date '+%Y-%m-%d %H:%M:%S') INFO: $1 \n"
}

print_logs_error () {
    printf "$(date '+%Y-%m-%#d %H:%M:%S') ERROR: $1 \n"
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

failfast () {
    exit 1
}

failfast_resetnode () {
    reset_csi_driver
    failfast
}

get_worker_node_num () {
    workerNodeNum=$(kubectl get nodes | grep Ready | wc -l)

    echo $workerNodeNum
}

get_running_pod () {
    podKeyword=$1
    podKeyword=${podKeyword:-""}
    pod=$(kubectl get po --all-namespaces -o wide --sort-by=.metadata.creationTimestamp | grep Running | grep $podKeyword | head -n 1)

    if  [ -z "$pod" ] 
    then
        echo "1" "" ""
    fi

    podName=$(echo $pod | awk '{print $2}')
    nodeName=$(echo $pod | awk '{print $8}')

    echo "0" "$podName" "$nodeName"
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
        failfast
    else
        print_logs_info "2 controller pods running..."        
    fi

    nodePodsNum=$(kubectl get pods -n kube-system --field-selector=status.phase=Running | grep "csi-azurelustre-node" | awk '{print $1}' | wc -l)
    workerNodeNum=$(get_worker_node_num)

    if  [ "$nodePodsNum" != "$workerNodeNum" ] 
    then
        print_logs_error "Expected node pods num $workerNodeNum, actual $nodePodsNum"
        failfast
    else
        print_logs_info "$nodePodsNum node pods running..."        
    fi
}