#!/bin/bash

# Copyright 2021 The Kubernetes Authors.
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

set -o errexit
set -o pipefail
set -o nounset
set -o xtrace

REPO_ROOT_PATH=${REPO_ROOT_PATH:-$(git rev-parse --show-toplevel)}
KUBECONFIG=${KUBECONFIG:-$(echo "$HOME/.kube/config")}
kubernetesVersion=${kubernetesVersion:-$(kubectl version -ojson | jq -r ".serverVersion.gitVersion")}
echo "kubectl path $(which kubectl)"
echo "REPO_ROOT_PATH ${REPO_ROOT_PATH}"
echo "KUBECONFIG path ${KUBECONFIG}"
echo "kubernetesVersion ${kubernetesVersion}"

curl -sL https://storage.googleapis.com/kubernetes-release/release/${kubernetesVersion}/kubernetes-test-linux-amd64.tar.gz --output ${REPO_ROOT_PATH}/e2e-tests.tar.gz
tar -xvf ${REPO_ROOT_PATH}/e2e-tests.tar.gz --directory ${REPO_ROOT_PATH}
rm ${REPO_ROOT_PATH}/e2e-tests.tar.gz

sc_file="${REPO_ROOT_PATH}/test/external-e2e/e2etest_storageclass.yaml"
claim_file="${REPO_ROOT_PATH}/test/external-e2e/test_claim.yaml"

echo "Generating test storageclass"
sed "s/{lustre_fs_name}/${LUSTRE_FS_NAME}/g;s/{lustre_mgs_ip}/${LUSTRE_MGS_IP}/g" ${sc_file}.template > ${sc_file}
echo "Generated storageclass"
cat ${sc_file}

clean_up_and_print_logs() {
    echo "clean up"
    kubectl delete -f ${claim_file} --ignore-not-found
    kubectl delete -f ${sc_file} --ignore-not-found
    echo "print out driver logs ..."
    bash ${REPO_ROOT_PATH}/utils/azurelustre_log.sh
}

trap clean_up_and_print_logs EXIT

mkdir -p /tmp/csi

# reclaim policy test

echo "begin to test reclaim policy"
echo "deploy test storageclass with default reclaim policy (delete)"
kubectl apply -f ${sc_file}
echo "deploy test pvc"
kubectl apply -f ${claim_file}
echo "wait pvc to Bound status"
# wait for json is supported in kubectl v1.24
kubectl wait --for=jsonpath='{.status.phase}'=Bound -f ${claim_file} --timeout=300s
bounded_pv=$(kubectl get -f ${claim_file} -ojsonpath='{.spec.volumeName}')
echo "bounded pv is ${bounded_pv}"
echo "delete pvc"
kubectl delete -f ${claim_file}
echo "wait for the pvc to be deleted"
kubectl wait --for=delete -f ${claim_file} --timeout=300s
echo "wait for pv ${bounded_pv} to be deleted"
kubectl wait --for=delete pv/${bounded_pv} --timeout=300s

echo "delete test storageclass"
kubectl delete -f ${sc_file}

echo "begin to run azurelustre tests ...."
cp ${REPO_ROOT_PATH}/test/external-e2e/e2etest_storageclass.yaml /tmp/csi/storageclass.yaml
${REPO_ROOT_PATH}/kubernetes/test/bin/ginkgo -p -v -focus="External.Storage.*.azurelustre.csi.azure.com" \
    -skip="should access to two volumes with the same volume mode and retain data across pod recreation on the same node|should support two pods which share the same volume|should be able to unmount after the subpath directory is deleted|should support two pods which share the same volume|Should test that pv written before kubelet restart is readable after restart|should unmount if pod is force deleted while kubelet is down|should unmount if pod is gracefully deleted while kubelet is down|should support two pods which have the same volume definition|should access to two volumes with the same volume mode and retain data across pod recreation on different node" \
    ${REPO_ROOT_PATH}/kubernetes/test/bin/e2e.test  -- \
    -storage.testdriver=${REPO_ROOT_PATH}/test/external-e2e/testdriver-azurelustre.yaml \
    --kubeconfig=${KUBECONFIG}
