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

echo $kube_config | base64 -d > kubeconfig

set -euo pipefail

# need to run in a container in github action to have root permission
apt update -y
apt install -y golang-ginkgo-dev
apt install -y --no-install-recommends git curl ca-certificates
update-ca-certificates

PROJECT_ROOT=$(git rev-parse --show-toplevel)

curl -sL https://storage.googleapis.com/kubernetes-release/release/v1.22.0/kubernetes-test-linux-amd64.tar.gz --output e2e-tests.tar.gz
tar -xvf e2e-tests.tar.gz && rm e2e-tests.tar.gz

print_logs() {
    echo "print out driver logs ..."
    bash ./test/utils/amlfs_log.sh $DRIVER
}

trap print_logs EXIT

mkdir -p /tmp/csi

if [ ! -z ${EXTERNAL_E2E_TEST_AMLFS} ]; then
    echo "begin to run amlfs tests ...."
    cp $PROJECT_POOT/test/external-e2e/e2etest_storageclass.yaml /tmp/csi/storageclass.yaml
    ginkgo -p --progress --v -focus="External.Storage.*.amlfs.csi.azure.com" \
        kubernetes/test/bin/e2e.test  -- \
        -skip="should access to two volumes with the same volume mode and retain data across pod recreation on the same node|should support two pods which share the same volume|should be able to unmount after the subpath directory is deleted|should support two pods which share the same volume|Should test that pv written before kubelet restart is readable after restart|should unmount if pod is force deleted while kubelet is down|should unmount if pod is gracefully deleted while kubelet is down"
        -storage.testdriver=$PROJECT_ROOT/test/external-e2e/testdriver-amlfs.yaml \
        --kubeconfig=$KUBECONFIG
fi
