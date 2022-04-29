#set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

source ./utils.sh

function print_versions () {
	nodepool=$(az aks nodepool show --resource-group $ResourceGroup --cluster-name $ClusterName --nodepool-name  $PoolName)
	currentNodeImageVersion=$(echo $nodepool | jq -r '.nodeImageVersion')

	nodepoolUpgrades=$(az aks nodepool get-upgrades --resource-group $ResourceGroup --cluster-name $ClusterName --nodepool-name $PoolName)
	nodeK8sVersion=$(echo $nodepoolUpgrades | jq -r '.kubernetesVersion')

	controlPlaneUpgrades=$(az aks get-upgrades --resource-group $ResourceGroup --name $ClusterName)
	currentControlPlaneK8sVersion=$(echo $controlPlaneUpgrades | jq -r '.controlPlaneProfile.kubernetesVersion')

	podName=$(kubectl get pods -n kube-system --field-selector=status.phase=Running --sort-by=.metadata.creationTimestamp | grep csi-azurelustre-node | awk '{print $1}' | head -n 1)
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

print_logs_info "Upgrading node image"
az aks nodepool upgrade --resource-group $ResourceGroup --cluster-name $ClusterName --name $PoolName --node-image-only

print_logs_info "Upgrading node kubernetes"
az aks nodepool upgrade --resource-group $ResourceGroup --cluster-name $ClusterName --name $PoolName

print_logs_info "Upgrading control-plane Kubernetes"
az aks upgrade --resource-group $ResourceGroup --name $ClusterName --yes

print_logs_title "Print versions after"
print_versions