#!/usr/bin/env bash
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

set -x
set -o errexit
set -o pipefail
set -o nounset

trap print_debug EXIT

REPO_ROOT_PATH=${REPO_ROOT_PATH:-$(git rev-parse --show-toplevel)}

export REPO_ROOT_PATH=${REPO_ROOT_PATH}
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
    echo -e "$(date '+%Y-%m-%d %H:%M:%S') ERROR: $1"
}

fast_exit () {
    print_debug
    exit 1
}

# OnDelete intentionally permits old revisions. RollingUpdate must finish its
# rollout as well as report Ready; Ready old pods alone are not a rollout barrier.
daemonset_readiness () {
    jq -r '
        (.status.desiredNumberScheduled // 0) as $desired
        | (.status.observedGeneration // 0) >= .metadata.generation
          and (.status.currentNumberScheduled // 0) == $desired
          and (.status.numberReady // 0) == $desired
          and (.status.numberAvailable // 0) == $desired
          and (.status.numberUnavailable // 0) == 0
          and (.status.numberMisscheduled // 0) == 0
          and (.spec.updateStrategy.type == "OnDelete"
               or (.spec.updateStrategy.type == "RollingUpdate"
                   and (.status.updatedNumberScheduled // 0) == $desired))
    ' <<<"$1"
}

# DaemonSetStatus has no updateRevision. Only a controller-owned history whose
# saved template matches the desired template identifies the desired pod hash.
# Empty output means the controller has not yet materialized an unambiguous match.
daemonset_desired_revision () {
    local daemonset_json=$1
    local histories
    histories=$(kubectl get controllerrevisions -n kube-system -o json) || return 1
    jq -r --argjson ds "${daemonset_json}" '
        [
            .items[]
            | select(any(.metadata.ownerReferences[]?;
                .kind == "DaemonSet" and .controller == true and .uid == $ds.metadata.uid))
            | select((.data.spec.template | del(."$patch")) == $ds.spec.template)
        ]
        | if length == 1 then .[0].metadata.labels["controller-revision-hash"] // ""
          else "" end
    ' <<<"${histories}"
}

wait_for_csi_driver_ready () {
    local timeout_seconds=${1:-600}
    local deadline=$((SECONDS + timeout_seconds))
    local daemonsets=(
        csi-azurelustre-node-jammy
        csi-azurelustre-node-noble
        csi-azurelustre-node-azurelinux3
    )

    kubectl rollout status -n kube-system deployment/csi-azurelustre-controller --timeout="${timeout_seconds}s" || return 1

    local daemonset
    for daemonset in "${daemonsets[@]}"; do
        local ready=false
        while (( SECONDS < deadline )); do
            local status
            status=$(kubectl get daemonset "${daemonset}" -n kube-system \
                -o json) || return 1
            ready=$(daemonset_readiness "${status}") || return 1
            if [[ "${ready}" == "true" ]]; then
                break
            fi
            sleep 5
        done

        if [[ "${ready}" != "true" ]]; then
            print_logs_error "timed out waiting for ${daemonset} pods to be ready"
            return 1
        fi
    done
}

reset_csi_driver () {
    echo "Reset CSI driver"

    # Delete all daemonsets with app=csi-azurelustre-node label (handles all flavors)
    for i in $(kubectl get daemonsets.apps -n kube-system -l app=csi-azurelustre-node -o name); do
        kubectl delete -n kube-system "${i}"
    done

    kubectl delete -f "${REPO_ROOT_PATH}"/deploy/csi-azurelustre-controller.yaml --ignore-not-found
    kubectl delete -f "${REPO_ROOT_PATH}"/deploy/csi-azurelustre-node-jammy.yaml --ignore-not-found
    kubectl delete -f "${REPO_ROOT_PATH}"/deploy/csi-azurelustre-node-noble.yaml --ignore-not-found
    kubectl delete -f "${REPO_ROOT_PATH}"/deploy/csi-azurelustre-node-azurelinux3.yaml --ignore-not-found
    kubectl wait pod -n kube-system --for=delete --selector='app in (csi-azurelustre-controller,csi-azurelustre-node)' --timeout=600s


    echo "Reset node label"
    kubectl get nodes --no-headers | grep "${PoolName}" | awk '{print $1}' |
    {
        while read -r n;
        do
            kubectl label nodes "${n}" node4faulttest-
        done
    }

    kubectl apply -f "${REPO_ROOT_PATH}"/deploy/csi-azurelustre-controller.yaml
    kubectl apply -f "${REPO_ROOT_PATH}"/deploy/csi-azurelustre-node-jammy.yaml
    kubectl apply -f "${REPO_ROOT_PATH}"/deploy/csi-azurelustre-node-noble.yaml
    kubectl apply -f "${REPO_ROOT_PATH}"/deploy/csi-azurelustre-node-azurelinux3.yaml

    wait_for_csi_driver_ready 600
}

get_worker_node_num () {
    workerNodeNum=$(kubectl get nodes | grep "${PoolName}" | grep -c Ready)

    echo "${workerNodeNum}"
}

get_pod_by_status () {
    podNameKeyword=${1:-""}
    podStatus=${2:-""}

    pod=$(kubectl get po --all-namespaces -o wide --sort-by=.metadata.creationTimestamp | grep "${PoolName}" | grep "${podStatus}" | grep "${podNameKeyword}" || true)

    if  [[ -z "${pod}" ]]
    then
        print_logs_error "can't find running pod with keyword=${podNameKeyword}"

        pod=$(get_pod "${podNameKeyword}")

        if  [[ -n "${pod}" ]]
        then
            podName=$(echo "${pod}" | awk '{print $2}')
            podStatus=$(echo "${pod}" | awk '{print $4}')
            print_logs_error "find pod ${podName} in ${podStatus} state, expect running"
        fi

        fast_exit
    else
         numOfPod=$(echo "${pod}" | grep -o -i "${podNameKeyword}" | wc -l)

        if [[ "${numOfPod}" != 1 ]]
        then
            print_logs_error "find ${numOfPod} running pod with keyword=${podNameKeyword}, expect only one"
        fi
    fi

    podName=$(echo "${pod}" | awk '{print $2}')
    nodeName=$(echo "${pod}" | awk '{print $8}')
    actualPodStatus=$(echo "${pod}" | awk '{print $4}')

    print_logs_info "workload pod ${podName} is running on ${nodeName}"

    local return_podName=$3
    local return_nodeName=$4
    local return_podStatus=$5

    printf -v "${return_podName}" '%s' "${podName}"
    printf -v "${return_nodeName}" '%s' "${nodeName}"
    printf -v "${return_podStatus}" '%s' "${actualPodStatus}"
}

get_pod_state () {
    podNameKeyword=${1:-""}
    nodeNameKeyword=${2:-""}

    state=$(kubectl get po --all-namespaces -o wide | grep "${PoolName}" | grep "${podNameKeyword}" | grep "${nodeNameKeyword}" | awk '{print $4}' | head -n 1 || true)
    echo "${state}"
}

get_pod () {
    podNameKeyword=${1:-""}
    nodeNameKeyword=${2:-""}

    pod=$(kubectl get po --all-namespaces -o wide | grep "${PoolName}" | grep "${podNameKeyword}" | grep "${nodeNameKeyword}" | head -n 1 || true)
    echo "${pod}"
}

verify_csi_driver () {
    controllerPodsNum=$(kubectl get po -n kube-system --field-selector=status.phase=Running | grep 'csi-azurelustre-controller' | awk '{print $1}' | wc -l)

    if  [[ "${controllerPodsNum}" != "2" ]]
    then
        print_logs_error "Expected controller pods num 2, actual ${controllerPodsNum}"
        fast_exit
    else
        print_logs_info "2 controller pods running..."
    fi

    nodePodsNum=$(kubectl get po -o wide -n kube-system -l app=csi-azurelustre-node --field-selector=status.phase=Running | grep -c "${PoolName}")
    workerNodeNum=$(get_worker_node_num)

    if  [[ "${nodePodsNum}" != "${workerNodeNum}" ]]
    then
        print_logs_error "Expected node pods num ${workerNodeNum}, actual ${nodePodsNum}"
        fast_exit
    else
        print_logs_info "${nodePodsNum} node pods running..."
    fi

    kubectl wait pod -n kube-system --for=condition=Ready --selector='app in (csi-azurelustre-controller,csi-azurelustre-node)' --timeout=600s

}

start_sample_workload () {
    stop_sample_workload
    kubectl apply -f ./sample-workload/deployment_write_print_file.yaml --timeout=600s
    if ! kubectl wait pod --for=condition=Ready --selector=app=azurelustre-longhaulsample-deployment --timeout=600s; then
        print_logs_error "Failed to start sample workload"
        print_debug
    fi
}

stop_sample_workload () {
    echo "Stop sample workload"
    if [[ -n $(kubectl get pvc azurelustre-longhaulsample-pvc --ignore-not-found) ]]; then
        kubectl patch pvc azurelustre-longhaulsample-pvc -p '{"metadata":{"finalizers":null}}'
    fi

    kubectl delete -f ./sample-workload/deployment_write_print_file.yaml --ignore-not-found --timeout=600s --grace-period=0 --force --cascade
    kubectl wait pod --for=delete --selector=app=azurelustre-longhaulsample-deployment --timeout=600s

}

verify_sample_workload_logs () {
    podName=$1
    lastOutput=$(kubectl logs "${podName}" | tail -n 1 | awk -F, '{print $1}')
    dateOfLastOutput=$(date -d "${lastOutput}" +%s)
    dateOfNow=$(date +%s)
    delta=$((dateOfNow-dateOfLastOutput))

    threshold=${2:-10}

    if [[ ${delta} -lt ${threshold} ]];
    then
        print_logs_info "currentDateTime=${dateOfNow}, lastOutput=${lastOutput}, lastOutputInSec=${dateOfLastOutput}. delta=${delta} is within threshold=${threshold}"
    else
        print_logs_error "currentDateTime=${dateOfNow}, lastOutput=${lastOutput}, lastOutputInSec=${dateOfLastOutput}. delta=${delta} is greater than threshold=${threshold}"
        fast_exit
    fi
}

verify_sample_workload_by_pod_status () {
    podStatus=${3:-'Running'}

    get_pod_by_status "${SampleWorkloadKeyword}" "${podStatus}" podName nodeName actualPodStatus

    if [[ "${actualPodStatus}" == "Running" ]]; then
        verify_sample_workload_logs "${podName}" "${TimeIntervalCheckLogInSecs}"
    fi

    local return_podName=$1
    local return_nodeName=$2
    printf -v "${return_podName}" '%s' "${podName}"
    printf -v "${return_nodeName}" '%s' "${nodeName}"
}

print_debug() {
    print_logs_title "Print DEBUG Start"

    bash "${REPO_ROOT_PATH}"/utils/azurelustre_log.sh

    print_logs_title "Print DEBUG End"
}

reset_all() {
    print_logs_title "RESET ALL Start"

    stop_sample_workload
    reset_csi_driver

    # Clear node labels left by fault-test.sh. Harmless if unset.
    kubectl label nodes --all node4faulttest- || true

    print_logs_title "RESET ALL End"
}
