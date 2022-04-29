set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

source ./utils.sh

export ClusterName="jusjin-csi-sub2"
export ResourceGroup="jusjin-csi-sub2"
export PoolName="longhaul"

print_logs_info "Connecting to AKS Cluster=$ClusterName, ResourceGroup=$ResourceGroup, AKS pool=$PoolName"
az configure --defaults group=$ResourceGroup
az aks get-credentials --resource-group $ResourceGroup --name $ClusterName

print_logs_case " Executing fault test"
./fault-test.sh

print_logs_case " Executing update test"
./update-test.sh