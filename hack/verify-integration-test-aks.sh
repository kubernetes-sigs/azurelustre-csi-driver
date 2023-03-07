#!/bin/bash

# Copyright 2022 The Kubernetes Authors.
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
echo "replace yaml file"
cat ./test/integration_aks/integration_test_aks.yaml.template \
  | sed "s/{test_acr_uri}/${test_acr_uri}/g;s/{lustre_fs_name}/${lustre_fs_name}/g;s/{lustre_fs_ip}/${lustre_fs_ip}/g" >./test/integration_aks/integration_test_aks.yaml
echo "done"

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" -o kubectl
chmod +x kubectl
export KUBECONFIG=$(pwd)/kubeconfig

./kubectl delete -f ./test/integration_aks/integration_test_aks.yaml --ignore-not-found
./kubectl apply -f ./test/integration_aks/integration_test_aks.yaml

function catlog {
    ./kubectl logs aml-integration-test
    ./kubectl delete -f ./test/integration_aks/integration_test_aks.yaml
}

trap catlog ERR EXIT

./kubectl wait --for=condition=Ready pod/aml-integration-test --timeout=60s
./kubectl wait --for=condition=Ready=false pod/aml-integration-test --timeout=300s

exit_code=$(./kubectl get pod aml-integration-test -o=jsonpath='{.status.containerStatuses[*].state.*.exitCode}')

exit $exit_code
