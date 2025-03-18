# Driver Parameters

These are the parameters to be passed into the custom StorageClass that users must create to use the CSI driver.

For more information, see the [Azure Managed Lustre Filesystem (AMLFS) service documentation](https://learn.microsoft.com/en-us/azure/azure-managed-lustre/) and the [AMLFS CSI documentation](https://learn.microsoft.com/en-us/azure/azure-managed-lustre/use-csi-driver-kubernetes).

## Dynamic Provisioning (Create an AMLFS Cluster through AKS)

### Permissions

The kubelet identity attached to the cluster will require the following permission actions (at the Subscription scope):

```text
Microsoft.Network/virtualNetworks/subnets/read
Microsoft.Network/virtualNetworks/subnets/join/action
Microsoft.StorageCache/getRequiredAmlFSSubnetsSize/action
Microsoft.StorageCache/checkAmlFSSubnets/action
Microsoft.StorageCache/amlFilesystems/read
Microsoft.StorageCache/amlFilesystems/write
Microsoft.StorageCache/amlFilesystems/delete
```

Alternatively, users can grant the identity the following broader roles:

- Reader permissions the Subscription scope
- Contributor permissions at the Resource Group scope (the Resource Group `resource-group-name` parameter of the StorageClass)
- Network Contributor at the Virtual Network scope (the Virtual Network specified in the `vnet-name` parameter of the StorageClass) instead of the individual permissions.

If using the `identities` parameter, users will also need to grant Managed Identity Operator role on the identity being assigned or the following permission action:

```text
Microsoft.ManagedIdentity/userAssignedIdentities/assign/action
```

### Parameters

Name | Meaning | Available Value | Mandatory | Default value
--- | --- | --- | --- | ---
sku-name | SKU name for the Azure Managed Lustre file system. The SKU determines the throughput of the AMLFS cluster. | The SKU value must be one of the following: `AMLFS-Durable-Premium-40`, `AMLFS-Durable-Premium-125`, `AMLFS-Durable-Premium-250`, `AMLFS-Durable-Premium-500`. | Yes | This value must be provided.
zones | The availability zone where your resource will be created. For the best performance, locate your AMLFS cluster in the same region and availability zone that houses your AKS cluster and other compute clients. | The zone must be a single value e.g., `"1"`, `"2"`, or `"3"`. | Yes | This value must be provided.
maintenance-day-of-week | The day of the week for maintenance to be performed on the AMLFS cluster. | `Sunday`, `Monday`, `Tuesday`, `Wednesday`, `Thursday`, `Friday`, `Saturday` | Yes | This value must be provided.
maintenance-time-of-day-utc | The time (in UTC) when the maintenance window can begin on the AMLFS cluster. | Time value can only be in 24-hour format i.e., HH:MM | Yes | This value must be provided.
location | Azure region in which the AMLFS cluster will be created. The region name should only have lower-case letters or numbers. | `eastus2`, `westus`, etc. | No | If empty, the driver will use the same region name as the current AKS cluster.
resource-group-name | The name of the resource group in which to create the AMLFS cluster. This resource group must already exist. | Resource group names can only include alphanumeric characters, underscores, parentheses, hyphens, periods (except at the end), and Unicode characters that match the allowed characters. | No | If empty, the driver will use the AKS infrastructure resource group.
vnet-resource-group | The name of the resource group containing the virtual network to be connected to the AMLFS cluster. This resource group must already exist. | Resource group names can only include alphanumeric characters, underscores, parentheses, hyphens, periods (except at the end), and Unicode characters that match the allowed characters. | No | If empty, the driver will use current AKS cluster's virtual network resource group
vnet-name | The name of the virtual network to be connected to the AMLFS cluster. This virtual network must already exist. Setup any virtual network peerings beforehand. | The name must begin with a letter or number, end with a letter, number, or underscore, and may contain only letters, numbers, underscores, periods, or hyphens. | No | If empty, the driver will use current AKS cluster's virtual network
subnet-name | The name of the subnet within the virtual network to be connected to the AMLFS cluster. This subnet must already exist. | The name must begin with a letter or number, end with a letter, number, or underscore, and may contain only letters, numbers, underscores, periods, or hyphens. | No | If empty, the driver will use current AKS cluster's subnet
identities | User-assigned identities to assign to the AMLFS cluster. These identities must already exist. | This must be the resource identifier for the identity e.g., `"/subscriptions/12345678-1234-1234-1234-123456789abc/resourceGroups/myResourceGroup/providers/Microsoft.ManagedIdentity/userAssignedIdentities/myManagedIdentity"`. Multiple values may be provided as a comma-separated list. | No | None
tags | Tags to apply to the AMLFS cluster resource. These tags do not affect AMLFS cluster functionality. | Tag format: `"key1=val1,key2=val2"`. The tag name has a limit of 512 characters and the tag value has a limit of 256 characters. Tag names can't contain these characters: `<, >, %, &, \, ?, /`. | No | None
sub-dir | This is the subdirectory within the AMLFS cluster's root directory which is where each pod will actually be mounted within the AMLFS filesystem. This subdirectory does not need to exist beforehand. | This must be a valid Linux file path. It can also interpret metadata such as `"${pvc.metadata.name}"`, `"${pvc.metadata.namespace}"`, `"${pv.metadata.name}"`, `"${pod.metadata.name}"`, `"${pod.metadata.namespace}"`, `"${pod.metadata.uid}"`.| No | None, will default to mounting the root directory of the AMLFS cluster.

## Static Provisioning (Bring your own AMLFS Cluster through AKS)

Name | Meaning | Available Value | Mandatory | Default value
--- | --- | --- | --- | ---
mgs-ip-address | The IP address of the Lustre MGS, see AMLFS cluster details. | Must be a valid IP address i.e., `x.x.x.x` | Yes | This value must be provided.
sub-dir | This is the subdirectory within the AMLFS cluster's root directory which is where each pod will actually be mounted within the AMLFS filesystem. This subdirectory does not need to exist beforehand. | This must be a valid Linux file path. It can also interpret metadata such as `"${pvc.metadata.name}"`, `"${pvc.metadata.namespace}"`, `"${pv.metadata.name}"`, `"${pod.metadata.name}"`, `"${pod.metadata.namespace}"`, `"${pod.metadata.uid}"`.| No | None, will default to mounting the root directory of the AMLFS cluster.
