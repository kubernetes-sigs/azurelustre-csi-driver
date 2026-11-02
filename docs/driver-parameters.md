# Driver Parameters

This reference covers driver installation settings, dynamic provisioning, and
connections to existing filesystems.

For more information, see the [Azure Managed Lustre Filesystem (AMLFS) service documentation](https://learn.microsoft.com/en-us/azure/azure-managed-lustre/) and the [AMLFS CSI documentation](https://learn.microsoft.com/en-us/azure/azure-managed-lustre/use-csi-driver-kubernetes).

## CSI Driver Configuration Parameters

These parameters control the behavior of the Azure Lustre CSI driver itself and are typically configured during driver installation rather than in StorageClass definitions.

### Driver Identity

For Helm installations, `csidriver.name` selects the CSI plugin identity. The
chart passes it to the controller and every node flavor and uses the same name
for the CSIDriver object, registration paths and uninstall guard. The default is
`azurelustre.csi.azure.com`, also used explicitly by the static manifests.

Configure a nondefault identity through `csidriver.name`, not by overriding
`--drivername` independently in `controller.extraArgs` or `node.extraArgs`.
Existing matching flags are accepted; conflicting or incomplete identity flags
fail chart rendering rather than silently selecting a different driver name.
StorageClasses and PVs must use that same CSI identity.

Keep the identity unchanged when upgrading an installation with existing
volumes. For an older custom configuration, make `csidriver.name` agree with
the identity the running driver and its PVs already use before upgrading.
Changing this value is not a volume-preserving rename. A configurable identity
does not make concurrent installations of this driver supported.

### Node Startup Taint Management

Name | Meaning | Available Value | Default Value | Configuration Method
--- | --- | --- | --- | ---
remove-not-ready-taint | Controls whether the CSI driver automatically removes startup taints from nodes when the driver becomes ready. This ensures pods are only scheduled to nodes where the CSI driver is fully operational and Lustre filesystem capacity is available. Nodes should have a taint of the form: `azurelustre.csi.azure.com/agent-not-ready:NoSchedule` | `true`, `false` | `true` | Command-line flag `--remove-not-ready-taint` in driver deployment

#### Startup Taint Details

When enabled (default), the Azure Lustre CSI driver will:

1. **Monitor Node Readiness**: Check if the CSI driver is fully initialized on the node
2. **Remove Blocking Taint**: Automatically remove the `azurelustre.csi.azure.com/agent-not-ready:NoSchedule` taint when ready

This mechanism prevents pods requiring Azure Lustre storage from being scheduled to nodes where:

- Lustre kernel modules are not yet loaded
- CSI driver components are not fully initialized
- Network connectivity to Lustre filesystems is not established

## Dynamic Provisioning (Create an AMLFS Cluster through AKS)

Dynamic provisioning creates an Azure Managed Lustre filesystem for a
PersistentVolumeClaim (PVC). Configure the filesystem's SKU, network and
maintenance settings in the StorageClass, and request its capacity in the PVC.
The driver obtains the filesystem's connection details automatically.

See the [dynamic provisioning guide](dynamic-provisioning.md) for the complete
setup and lifecycle instructions.

### Permissions For Kubelet Identity

See [Use a managed identity in Azure Kubernetes Service (AKS)](https://learn.microsoft.com/en-us/azure/aks/use-managed-identity) for information about configuring your kubelet identity.

Grant these required permissions:

Action | Assignment scope
--- | ---
`Microsoft.StorageCache/amlFilesystems/read` | Filesystem resource group (`resource-group-name`)
`Microsoft.StorageCache/amlFilesystems/write` | Filesystem resource group (`resource-group-name`)
`Microsoft.StorageCache/amlFilesystems/delete` | Filesystem resource group (`resource-group-name`)
`Microsoft.Network/virtualNetworks/subnets/join/action` | Filesystem subnet or its containing virtual network

Alternatively, assign both broader built-in roles:

- Contributor on the filesystem resource group.
- Network Contributor on the filesystem's virtual network.

Allow several minutes for role assignments to take effect before provisioning.

#### Optional fail-fast checks

For early SKU and subnet-capacity validation, also grant:

Check | Action | Assignment scope
--- | --- | ---
SKU lookup | `Microsoft.StorageCache/skus/read` | Subscription containing the filesystem
Required subnet size | `Microsoft.StorageCache/getRequiredAmlFSSubnetsSize/*` | Subscription containing the filesystem
Available subnet IP addresses | `Microsoft.Network/virtualNetworks/usages/read` | Virtual network containing the filesystem subnet

Assign the subscription-scoped actions through a separate custom role.

Missing any combination of these permissions does not prevent provisioning.
Azure still validates the filesystem creation request.
See [SKU discovery and capacity validation](dynamic-provisioning.md#sku-discovery-and-capacity-validation).

#### Filesystem identity assignment

When using `identities`, also grant Managed Identity Operator on each assigned
identity, or this action:

```text
Microsoft.ManagedIdentity/userAssignedIdentities/assign/action
```

### Workload Identity (Alternative to Kubelet Identity)

Instead of granting these permissions to the kubelet (node) identity, the
controller can authenticate to Azure using Microsoft Entra Workload Identity,
with the permissions above assigned to a user-assigned managed identity that is
federated with the controller ServiceAccount. See
[Workload Identity](workload-identity.md) for setup.

### Parameters

Name | Meaning | Available Value | Mandatory | Default value
--- | --- | --- | --- | ---
sku-name | SKU name for the Azure Managed Lustre file system. The SKU determines the throughput of the AMLFS cluster. | The SKU value must be one of the following: `AMLFS-Durable-Premium-40`, `AMLFS-Durable-Premium-125`, `AMLFS-Durable-Premium-250`, `AMLFS-Durable-Premium-500`. | Yes | This value must be provided.
zone | The availability zone where your resource will be created. For the best performance, locate your AMLFS cluster in the same region and availability zone that houses your AKS cluster and other compute clients. | The zone must be a single value e.g., `"1"`, `"2"`, or `"3"`. | When required by the region | No default.
maintenance-day-of-week | The day of the week for maintenance to be performed on the AMLFS cluster. | `Sunday`, `Monday`, `Tuesday`, `Wednesday`, `Thursday`, `Friday`, `Saturday` | Yes | This value must be provided.
maintenance-time-of-day-utc | The time (in UTC) when the maintenance window can begin on the AMLFS cluster. | Time value can only be in 24-hour format i.e., HH:MM | Yes | This value must be provided.
location | Azure region in which the AMLFS cluster will be created. The region name should only have lower-case letters or numbers. | `eastus2`, `westus`, etc. | No | If empty, the driver will use the same region name as the current AKS cluster.
resource-group-name | The name of the resource group in which to create the AMLFS cluster. This resource group must already exist. | Resource group names can only include alphanumeric characters, underscores, parentheses, hyphens, periods (except at the end), and Unicode characters that match the allowed characters. | No | If empty, the driver will use the AKS infrastructure resource group.
vnet-resource-group | The name of the resource group containing the virtual network to be connected to the AMLFS cluster. This resource group must already exist. | Resource group names can only include alphanumeric characters, underscores, parentheses, hyphens, periods (except at the end), and Unicode characters that match the allowed characters. | No | If empty, the driver will use current AKS cluster's virtual network resource group
vnet-name | The name of the virtual network to be connected to the AMLFS cluster. This virtual network must already exist. Setup any virtual network peerings beforehand. | The name must begin with a letter or number, end with a letter, number, or underscore, and may contain only letters, numbers, underscores, periods, or hyphens. | No | If empty, the driver will use current AKS cluster's virtual network
subnet-name | The name of the subnet within the virtual network to be connected to the AMLFS cluster. This subnet must already exist. | The name must begin with a letter or number, end with a letter, number, or underscore, and may contain only letters, numbers, underscores, periods, or hyphens. | No | If empty, the driver will use current AKS cluster's subnet
identities | User-assigned identities to assign to the AMLFS cluster. These identities must already exist. | This must be the resource identifier for the identity e.g., `"/subscriptions/12345678-1234-1234-1234-123456789abc/resourceGroups/myResourceGroup/providers/Microsoft.ManagedIdentity/userAssignedIdentities/myManagedIdentity"`. Multiple values may be provided as a comma-separated list. | No | None
tags | Tags to apply to the AMLFS cluster resource. These tags do not affect AMLFS cluster functionality. | Tag format: `"key1=val1,key2=val2"`. The tag name has a limit of 512 characters and the tag value has a limit of 256 characters. Tag names can't contain these characters: `<, >, %, &, \, ?, /`. | No | None
sub-dir | Directory within the AMLFS filesystem to mount in the pod. Writable mounts create it if needed; read-only mounts require it to exist. See [subdirectory creation](#subdirectory-creation). | A path within the filesystem root; `#` is not allowed in new provisioning requests. Supports metadata such as `"${pvc.metadata.name}"`, `"${pvc.metadata.namespace}"`, `"${pv.metadata.name}"`, `"${pod.metadata.name}"`, `"${pod.metadata.namespace}"`, `"${pod.metadata.uid}"`. | No | None, will default to mounting the root directory of the AMLFS cluster.

### Subdirectory creation

For both static and dynamic provisioning, a writable mount creates the configured
`sub-dir` if it does not already exist. Creation resolves paths relative to the
mounted Lustre filesystem root. Path resolution outside that root is rejected.

The configured `sub-dir` must use directory components, not symlinks. Lustre
cannot mount a subdirectory through a symlink, even when a relative symlink stays
within the same filesystem. Use the target's directory path instead. For a new
writable mount, the driver checks existing path components and returns
`FailedPrecondition` if it finds a symlink, before creating the subdirectory.
This does not affect ordinary symlink use inside a mounted volume.

Directories created during an unsuccessful mount are not automatically removed.

Read-only mounts do not create subdirectories; the configured path must already
exist. Existing mounts are not rechecked.
Creation does not change permissions on existing directories.
Administrators must still set appropriate Lustre directory permissions for
shared storage.

For a new mount, a readonly CSI request or a standalone `ro` mount option takes
precedence over contradictory writable options. The driver keeps the supplied
options and adds a final `ro` when needed. This also keeps subdirectory-creation
behavior consistent with the readonly mount request; other Lustre options are
not restricted by a driver allowlist.

### Controller override for incomplete SKU zone metadata

Use this controller-only override when AMLFS requires a zone but the SKU
metadata advertises no zones for that region.

For a Helm installation, set `controller.extraArgs` in the Helm values:

```yaml
controller:
  extraArgs:
    - "-v=5"
    - "--allow-unadvertised-zones"
```

For a direct manifest installation, add the flag to the `azurelustre`
container in `deploy/csi-azurelustre-controller.yaml`:

```yaml
args:
  - -v=5
  - --allow-unadvertised-zones
  - --endpoint=$(CSI_ENDPOINT)
```

Apply the updated controller manifest after making this change. Do not add the
flag to the node DaemonSets, CSI provisioner sidecar, or StorageClass.

Set `zone` in the StorageClass to a value supported by AMLFS. The override
permits an explicit zone only when SKU metadata advertises no zones; it does
not bypass validation of a non-empty zone list. Leave it disabled otherwise,
and remove it once the metadata advertises the region's zones.

This override is not needed when SKU discovery itself fails. In that case the
driver passes the configured zone to Azure for validation.

## Static Provisioning (Bring your own AMLFS Cluster through AKS)

Static provisioning connects workloads to an existing filesystem. Supply its MGS
IP address using `mgs-ip-address`; dynamic provisioning obtains this information
automatically.

Name | Meaning | Available Value | Mandatory | Default value
--- | --- | --- | --- | ---
mgs-ip-address | The IP address of the Lustre MGS, see AMLFS cluster details. | Must be a valid IP address i.e., `x.x.x.x` | Yes | This value must be provided.
sub-dir | Directory within the AMLFS filesystem to mount in the pod. Writable mounts create it if needed; read-only mounts require it to exist. See [subdirectory creation](#subdirectory-creation). | A path within the filesystem root; `#` is not allowed in new provisioning requests. Supports metadata such as `"${pvc.metadata.name}"`, `"${pvc.metadata.namespace}"`, `"${pv.metadata.name}"`, `"${pod.metadata.name}"`, `"${pod.metadata.namespace}"`, `"${pod.metadata.uid}"`. | No | None, will default to mounting the root directory of the AMLFS cluster.
