set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

pushd "./test/long-haul/"
source ./utils.sh

export ClusterName="${aks_cluster_name}"
export ResourceGroup="${aks_resource_group}"
export PoolName="${aks_pool_name}"

print_logs_info "Connecting to AKS Cluster=$ClusterName, ResourceGroup=$ResourceGroup, AKS pool=$PoolName"
az configure --defaults group=$ResourceGroup
az aks get-credentials --resource-group $ResourceGroup --name $ClusterName

print_logs_case " Executing fault test"
./fault-test.sh

print_logs_case " Executing update test"
./update-test.sh