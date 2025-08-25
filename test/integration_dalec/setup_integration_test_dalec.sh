#!/bin/bash

set -euox pipefail

# Ensure required variables are set
: "${IMAGE_NAME:?Required variable IMAGE_NAME is not set}" # Ex: upstream.azurecr.io/oss/v2/kubernetes-csi/azurelustre-csi:v0.3.0

echo "IMAGE_NAME: ${IMAGE_NAME}"

# Create a configmap for the integration test script
kubectl delete configmap integration-dalec-script --ignore-not-found
kubectl create configmap integration-dalec-script --from-file=run_integration_test.sh

# Show the filled in template
envsubst < integration_dalec_aks.yaml.template

# Make sure to delete any previous instances of the pod
envsubst < integration_dalec_aks.yaml.template | kubectl delete -f - --ignore-not-found
envsubst < integration_dalec_aks.yaml.template | kubectl apply -f -

# Now running - wait for completion!
pod=azurelustre-integration-dalec
kubectl wait --for=condition=Ready pod/${pod} --timeout=300s
kubectl wait --for=condition=Ready=false pod/${pod} --timeout=300s
# Grab Result.
result=$(kubectl get pod ${pod} -o=jsonpath='{.status.containerStatuses[*].state.*.exitCode}')
kubectl logs ${pod}
echo "Result: ${result}"
exit ${result}
