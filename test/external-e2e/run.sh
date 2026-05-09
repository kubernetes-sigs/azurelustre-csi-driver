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

curl -sL https://dl.k8s.io/release/${kubernetesVersion}/kubernetes-test-linux-amd64.tar.gz --output ${REPO_ROOT_PATH}/e2e-tests.tar.gz
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

    # Delete test PVC (from reclaim policy test) and wait for it to be gone
    kubectl delete -f ${claim_file} --ignore-not-found --wait=true --timeout=300s || true

    # Collect the set of PVs created by this test (filtered by the test
    # StorageClass) so we can scope VolumeAttachment + PV cleanup to just
    # those, rather than every Azure Lustre PV in the cluster (which would
    # also nuke the long-haul sample workload PV if it's running).
    # `|| true` on the substitution guards against transient apiserver
    # errors aborting the trap under errexit.
    test_pvs=$(kubectl get pv -o jsonpath='{.items[?(@.spec.storageClassName=="testazurelustre.csi.azure.com")].metadata.name}' || true)

    # Clean up VolumeAttachments for our test PVs BEFORE attempting PV
    # deletion -- VolumeAttachments block PV detach/delete, so a stuck VA
    # would hang each subsequent `kubectl delete pv` for its full timeout.
    # Skip entirely if no test PVs exist (avoids an empty-string false match
    # in the substring check below, where empty va_pv against empty test_pvs
    # would otherwise match the literal "  " pattern).
    if [[ -n "${test_pvs}" ]]; then
        for va in $(kubectl get volumeattachment -o jsonpath='{.items[?(@.spec.attacher=="azurelustre.csi.azure.com")].metadata.name}' || true); do
            va_pv=$(kubectl get volumeattachment "${va}" -o jsonpath='{.spec.source.persistentVolumeName}' || true)
            if [[ -n "${va_pv}" && " ${test_pvs} " == *" ${va_pv} "* ]]; then
                echo "Deleting leftover VolumeAttachment: ${va} (PV ${va_pv})"
                kubectl delete volumeattachment "${va}" --ignore-not-found --timeout=120s || true
            fi
        done
    fi

    # Wait for any PVs created by this test to be cleaned up. These are
    # cluster-scoped and may linger after PVC deletion if the reclaim
    # policy is Retain, or if PV deletion is slow.
    for pv in ${test_pvs}; do
        echo "Waiting for PV deletion: ${pv}"
        kubectl delete "pv/${pv}" --ignore-not-found --wait=true --timeout=300s || true
    done

    # Delete StorageClass used by the tests
    kubectl delete -f ${sc_file} --ignore-not-found --wait=true || true

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
kubectl wait --for=jsonpath='{.status.phase}'=Bound -f ${claim_file} --timeout=600s
bounded_pv=$(kubectl get -f ${claim_file} -ojsonpath='{.spec.volumeName}')
echo "bounded pv is ${bounded_pv}"
echo "delete pvc"
kubectl delete -f ${claim_file}
echo "wait for the pvc to be deleted"
kubectl wait --for=delete -f ${claim_file} --timeout=600s
echo "wait for pv ${bounded_pv} to be deleted"
kubectl wait --for=delete pv/${bounded_pv} --timeout=600s

echo "delete test storageclass"
kubectl delete -f ${sc_file}

# Skip of "SELinuxMountReadWriteOncePod" can be removed when we begin testing against Kubernetes version 1.27+
echo "begin to run azurelustre tests ...."
cp ${REPO_ROOT_PATH}/test/external-e2e/e2etest_storageclass.yaml /tmp/csi/storageclass.yaml
${REPO_ROOT_PATH}/kubernetes/test/bin/ginkgo -p -v -focus="External.Storage.*.azurelustre.csi.azure.com" \
    ${REPO_ROOT_PATH}/kubernetes/test/bin/e2e.test  -- \
    -storage.testdriver=${REPO_ROOT_PATH}/test/external-e2e/testdriver-azurelustre.yaml \
    --kubeconfig=${KUBECONFIG}
