#!/bin/bash

set -o errexit
set -o pipefail
set -o nounset

work_path=$(dirname $0)
log_path=${work_path}/logs
target_yaml="scale_test_static.yaml"
echo "work path ${work_path}"
echo "log path ${log_path}"
echo "target yaml file ${target_yaml}"

echo "= setup"
echo "== clenup old workload"
kubectl delete -f ${work_path}/${target_yaml} --ignore-not-found

echo "== clenup old logs"
rm -rf ${log_path} || true

echo "== create log path ${log_path}"
mkdir ${log_path}
mkdir ${log_path}/csi_controller
mkdir ${log_path}/csi_node
mkdir ${log_path}/pods

echo "== reinstall driver"
echo "=== uninstall driver"
./${work_path}/../../deploy/uninstall-driver.sh

echo "=== install driver"
./${work_path}/../../deploy/install-driver.sh

echo "= begin test"
begin_test_time=$(date "+%Y-%m-%d %H:%M:%S.%3N")
echo "begin time ${begin_test_time}"
echo "begin_test_time: ${begin_test_time}" >>${log_path}/test_time

echo "== deploy workload"
kubectl apply -f ${work_path}/${target_yaml}

echo "== registry cleanup handler"
function catlog_and_cleanup {
    echo "= fetch log and cleanup"

    echo "== delete workload"
    begin_delete_time=$(date "+%Y-%m-%d %H:%M:%S.%3N")
    echo "begin delete time ${begin_delete_time}"
    echo "begin_delete_time: ${begin_delete_time}" >>${log_path}/test_time
    kubectl delete -f ${work_path}/${target_yaml} --ignore-not-found
    echo "== wait for worload deleted"
    kubectl wait --for=delete pod selector=app=csi-scale-test
    end_delete_time=$(date "+%Y-%m-%d %H:%M:%S.%3N")
    echo "end delete time ${end_delete_time}"
    echo "end_delete_time: ${end_delete_time}" >>${log_path}/test_time

    echo "== fetch logs"
    echo "=== fetch controller logs"
    controller_pods=$(kubectl get -nkube-system pods --selector=app=csi-azurelustre-controller --no-headers | awk '{print $1}')
    for controller_pod in $controller_pods
    do
        echo "==== fetch logs from ${controller_pod}"
        kubectl logs -nkube-system ${controller_pod} -cazurelustre >${log_path}/csi_controller/${controller_pod}
    done

    echo "=== fetch node logs"
    node_pods=$(kubectl get -nkube-system pods --selector=app=csi-azurelustre-node --no-headers | awk '{print $1}')
    for node_pod in $node_pods
    do
        echo "==== fetch logs from ${node_pod}"
        kubectl logs -nkube-system ${node_pod} -cazurelustre >${log_path}/csi_node/${node_pod}
    done

    end_test_time=$(date "+%Y-%m-%d %H:%M:%S.%3N")
    echo "end test time ${end_test_time}"
    echo "end_test_time: ${end_test_time}" >>${log_path}/test_time
}

trap catlog_and_cleanup ERR EXIT

echo "== wait for worload ready"
kubectl wait --for=condition=Ready pod --selector=app=csi-scale-test --timeout=300s
pods_ready_time=$(date "+%Y-%m-%d %H:%M:%S.%3N")
echo "pods ready time ${pods_ready_time}"
echo "pods_ready_time: ${pods_ready_time}" >>${log_path}/test_time

sleep 10
