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

#!/bin/bash

set -o errexit
set -o pipefail
set -o nounset

source ./utils.sh

trap print_debug ERR

function print_versions () {
	# Give extra one minute for daemonset pod to install client modules
	sleep 60
	
	nodepool=$(az aks nodepool show --resource-group $ResourceGroup --cluster-name $ClusterName --nodepool-name $PoolName)
	currentNodeImageVersion=$(echo $nodepool | jq -r '.nodeImageVersion')

	nodepoolUpgrades=$(az aks nodepool get-upgrades --resource-group $ResourceGroup --cluster-name $ClusterName --nodepool-name $PoolName)
	nodeK8sVersion=$(echo $nodepoolUpgrades | jq -r '.kubernetesVersion')

	controlPlaneUpgrades=$(az aks get-upgrades --resource-group $ResourceGroup --name $ClusterName)
	currentControlPlaneK8sVersion=$(echo $controlPlaneUpgrades | jq -r '.controlPlaneProfile.kubernetesVersion')

	podName=$(kubectl get pods -n kube-system -o wide --field-selector=status.phase=Running --sort-by=.metadata.creationTimestamp | grep csi-azurelustre-node | grep $PoolName | awk '{print $1}' | head -n 1)
	echo "Get kernel version and Lustre module version from pod $podName"
	kernelVersion=$(kubectl exec -n kube-system -it $podName -c azurelustre -- /bin/bash -c "uname -r")
	module=$(kubectl exec -n kube-system -it $podName -c azurelustre -- /bin/bash -c "dpkg-query -f '\${Package}|\${Version}' -W lustre-client-modules-*")
	modulePkgName=${module%|*}
	modulePkgVersion=${module#*|}

	print_logs_info "Node image version: $currentNodeImageVersion"
	print_logs_info "Node Kubernetes version: $nodeK8sVersion"
	print_logs_info "Control-plane Kubernetes version: $currentControlPlaneK8sVersion"
	print_logs_info "OS kernel version: $kernelVersion"
	print_logs_info "Lustre client module package name: $modulePkgName"
	print_logs_info "Lustre client module package version: $modulePkgVersion"	
}

print_logs_title "Print versions before"
print_versions

kubernetesUpgrades=$(az aks get-upgrades --resource-group $ResourceGroup --name $ClusterName | jq -r .controlPlaneProfile.upgrades)

if [[ "$kubernetesUpgrades" != "null" ]]; then
	latestKubernetesVersion=$(echo "$kubernetesUpgrades" | jq -r .[].kubernetesVersion | tail -n 1)

	print_logs_info "Upgrading Kubernetes control-plane"
	az aks upgrade --resource-group $ResourceGroup --name $ClusterName --yes --kubernetes-version $latestKubernetesVersion
else
	echo "Kubernetes control-plane version is the latest"
fi

print_logs_info "Upgrading node pool"
az aks nodepool upgrade --resource-group $ResourceGroup --cluster-name $ClusterName --name $PoolName

print_logs_title "Print versions after"
print_versions

samplePod=$(get_pod "azurelustre-longhaulsample-deployment")

if [[ ! -z "$samplePod" ]]
then
	podName=$(echo "$samplePod" | awk '{print $2}')
	podStatus=$(echo "$samplePod" | awk '{print $4}')
	print_logs_error "find pod $samplePod in $podStatus state, expect no running sample pod"
fi

print_logs_title "Start and verify sample workload"
sleep 60

start_sample_workload

verify_sample_workload_by_pod_status workloadPodName workloadNodeName