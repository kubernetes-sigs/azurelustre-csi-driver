#!/bin/bash

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

./kubectl delete -f ./test/integration_aks/integration_test_aks.yaml || true
./kubectl apply -f ./test/integration_aks/integration_test_aks.yaml

function catlog {
    ./kubectl logs aml-integration-test
    ./kubectl delete -f ./test/integration_aks/integration_test_aks.yaml
}

trap catlog ERR EXIT

./kubectl wait --for=condition=Ready pod/aml-integration-test --timeout=60s
./kubectl wait --for=condition=Ready=false pod/aml-integration-test --timeout=120s

exit_code=$(./kubectl get pod aml-integration-test -o=jsonpath='{.status.containerStatuses[*].state.*.exitCode}')

exit $exit_code
