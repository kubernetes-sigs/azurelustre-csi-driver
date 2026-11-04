# Dynamic Provisioning

This document explains how to deploy your workload to a dynamically provisioned Azure Managed Lustre cluster
using the Lustre CSI driver.

## Prerequisites

### Permissions

Before provisioning, grant the [required permissions](driver-parameters.md#permissions-for-kubelet-identity)
to the controller's Azure identity: the kubelet identity by default, or the
federated user-assigned identity when [workload identity](workload-identity.md)
is enabled. The permissions reference also lists optional fail-fast checks.

### SKU discovery and capacity validation

When information about the requested SKU is available, the driver validates the
zone and rounds the PVC's requested capacity up to that SKU's allocation increment,
enforcing its maximum size.

If that information is unavailable or unusable, the driver logs a warning and
continues with the configured SKU and zone. Capacity is rounded up in 4-TiB
increments. Azure validates the creation request and may reject unsupported
values.

The provisioned capacity can be larger than the size requested in the PVC. See
[SKU retrieval warnings](errors.md#warning-sku-retrieval-failures) for
troubleshooting.

Subnet-capacity checks are also optional when their permissions are missing.
To enable early validation, grant the [optional fail-fast permissions](driver-parameters.md#optional-fail-fast-checks).
Other subnet-check errors and known insufficient capacity still block creation.

### Azure cloud environment

AKS creates `/etc/kubernetes/azure.json` for the cluster. The driver reads this
file from the controller and node pods, so users normally do not need to create
or edit it. The `cloud` field is a cluster-level setting supplied by AKS, not a
StorageClass parameter.

For reference, the standard cloud values configure the ARM endpoint and token
audience used by the Azure SDK:

| `cloud` value | ARM endpoint | Token audience |
| --- | --- | --- |
| `AzurePublicCloud` | `https://management.azure.com` | `https://management.core.windows.net/` |
| `AzureChinaCloud` | `https://management.chinacloudapi.cn` | `https://management.core.chinacloudapi.cn/` |

The raw manifests and Helm chart mount the AKS-provided file read-only. No
additional cloud setting is required in the StorageClass or Helm values.

### Network Egress

> [!IMPORTANT]
> The CSI driver controller pod requires outbound HTTPS access to Azure Resource Manager
> and Microsoft Entra ID for dynamic provisioning operations (creating, deleting, and querying
> AMLFS clusters). The endpoints must match the configured Azure cloud:
>
> Azure Public: `management.azure.com`, `login.microsoftonline.com`
> Azure China: `management.chinacloudapi.cn`, `login.chinacloudapi.cn`
>
> The controller pod does **not** use host networking, so it relies on the cluster's pod
> network egress path. If your AKS cluster restricts outbound traffic (e.g., private clusters,
> UDR-based egress, or clusters created after March 2026 when AKS
> [retired default outbound access](https://learn.microsoft.com/en-us/azure/aks/outbound-rules-control-egress)),
> you must ensure that the controller pod can reach these endpoints through a NAT Gateway,
> Azure Load Balancer, or firewall/proxy rule.
>
> **Static provisioning is not affected** — the node pod mounts Lustre filesystems directly
> over the host network and does not require these Azure management endpoints.

The filesystem's subnet must also meet the
[AMLFS network prerequisites](https://learn.microsoft.com/en-us/azure/azure-managed-lustre/amlfs-prerequisites#network-prerequisites),
including outbound connectivity from that subnet. Controller connectivity alone
does not satisfy the filesystem's networking requirements.

## Create an Azure Managed Lustre cluster bound to a Persistent Volume Claim

### Configure the StorageClass

* Download
[dynamic provision storage class](./examples/storageclass_dynprov_lustre.yaml)
and [persistent volume claim file](./examples/pvc_storageclass_dynprov.yaml).

> [!IMPORTANT]
> The example uses `reclaimPolicy: Delete`: deleting a bound claim deletes its
> filesystem and data. To preserve it, set `reclaimPolicy: Retain` in the
> StorageClass before creating the claim.

* Edit the `SKU_NAME`, `ZONE`, `MAINTENANCE_DAY`, and `MAINTENANCE_TIME_OF_DAY` in the storage class.
  Omit `zone` when the selected region does not support availability zones.
  * By default, the filesystem uses the AKS location, infrastructure resource group, and subnet.
  You can choose an existing location,
  resource group, and subnet for the cluster to be deployed into by setting the `LOCATION`, `RESOURCE_GROUP_NAME`,
  `EXISTING_VNET_RG`, `EXISTING_VNET_NAME`, and/or `EXISTING_SUBNET_NAME` values in the storage class.
  * You can optionally set user-assigned identities, tags, and/or the subdirectory template for
  pods to use by setting the `IDENTITIES`, `TAGS`, and/or the `SUBDIRECTORY` values in the storage class.
* Set `resources.requests.storage` in the PVC to the required capacity.

Each PVC creates a separate filesystem. The example also includes an optional
`ResourceQuota` that limits this StorageClass to one PVC in the namespace where
you apply it. Adjust the quota before creating more claims. Your Azure
subscription may have a separate filesystem-count quota.

### Create the claim

```shell
kubectl create -f storageclass_dynprov_lustre.yaml
kubectl create -f pvc_storageclass_dynprov.yaml
```

> [!WARNING]
> Keep the PVC and driver in place until the PVC is `Bound`. Deleting a pending
> PVC does not cancel Azure creation and can leave a billable filesystem without
> a PV; see [unfinished provisioning](#deleting-a-claim-before-provisioning-finishes).
>
> Do not use or independently modify the filesystem before the PVC is `Bound`.
> The driver can replace a failed backend while provisioning is unfinished,
> which would destroy any data placed there early.

* The Azure Managed Lustre cluster will be created with the same name as the persistent volume it is
associated with. In addition to any tags configured in the storage class, it will also have tags added
for administrative convenience when working directly with the cluster such as through the Azure
portal. The following is an example of these values:
  * Azure Managed Lustre cluster name (example): `pvc-78876f95-32c2-41c4-bdfa-eb92d1eeb341`
  * Additional tags (example):

    ```yaml
    k8s-azure-created-by: kubernetes-azurelustre-csi-driver
    kubernetes.io-created-for-pv-name: pvc-78876f95-32c2-41c4-bdfa-eb92d1eeb341
    kubernetes.io-created-for-pvc-name: pvc-lustre-dynprov
    kubernetes.io-created-for-pvc-namespace: default
    ```

Azure permits [at most 50 tags per resource](https://learn.microsoft.com/azure/azure-resource-manager/management/tag-resources#limitations).
Driver-added tags count toward that limit; the usual four metadata tags leave
room for 46 custom tags. Tag names are case-insensitive, and repeated names use
the last supplied value. Driver-reserved tag names cannot be supplied in any
casing. The `tags` parameter keeps its comma-separated `key=value` format;
commas and additional equals signs inside a pair are not escaped by this format.

### Wait for provisioning

Creation may take ten minutes or more. Monitor the claim's status and events:

```shell
kubectl describe pvc pvc-lustre-dynprov
```

The PVC remains `Pending` while provisioning and becomes `Bound` when its volume
is ready. The driver reports success only for a compatible filesystem with
provisioning state `Succeeded`, health `Available`, and valid connection details.
Retries can extend the creation time.

If errors persist, use the [dynamic provisioning error reference](errors.md#dynamic-provisioning-errors).
When events or logs do not explain a failure, check the
[Azure Activity Log](errors.md#error-amlfs-provisioning-failed).
Retries do not resolve a persistent configuration or service problem.

### Creation retries and existing filesystems

On retry, the driver checks the filesystem's current Azure state. It reuses a
compatible, ready filesystem without changing its tags or maintenance settings,
and reports its current capacity.

For a matching `Failed` filesystem belonging to an unfinished provisioning
request, the driver deletes it, waits for deletion to finish, and recreates it
on retry. Recovery can resume after a controller restart while the PVC exists.
A timeout, lost response, or health problem alone does not trigger deletion.

## Use the Volume

* Download [dynamic provisioning demo pod echo date](./examples/pod_echo_date_dynprov.yaml)

* After the PVC is `Bound`, create the pod that mounts it:

```shell
kubectl create -f pod_echo_date_dynprov.yaml
```

* Confirm that the volume is mounted:

```shell
kubectl exec lustre-echo-date-dynprov -- df -h /mnt/lustre
```

&nbsp;

## Delete the Volume

The following steps apply to a provisioned volume. For a claim that is still
`Pending`, read [Deleting a claim before provisioning finishes](#deleting-a-claim-before-provisioning-finishes).

1. Stop workloads using the volume and wait for their mounts to be removed.
   Keep the driver running until cleanup finishes.
2. Find the PV and check its reclaim policy:

   ```shell
   kubectl get pvc pvc-lustre-dynprov -o wide
   kubectl get pv <pv-name>
   ```

   The **existing PV's** policy controls reclamation. To preserve the filesystem,
   [set that PV to `Retain`](https://kubernetes.io/docs/tasks/administer-cluster/change-pv-reclaim-policy/)
   before deleting the claim. Changing the StorageClass alone does not update it.

3. Delete the claim once you have confirmed the intended policy:

   ```shell
   kubectl delete pvc pvc-lustre-dynprov
   ```

4. With `Delete`, wait for the Azure filesystem and PV to be deleted; this may
   take ten minutes or more. With `Retain`, the filesystem remains and continues
   to incur charges. Save its identity for later management or
   [reattachment](static-provisioning.md#option-2-use-pv).

If cleanup stalls, inspect PV events and controller logs with the driver still
running. Do not remove PV finalizers to force progress.

### Deleting a claim before provisioning finishes

Deleting a pending PVC, including through namespace deletion, does not cancel
Azure creation. The filesystem can remain billable without a PV if the unfinished
request is lost. After an interrupted or deleted request, follow
[Deleted pending PVC or untracked filesystem](errors.md#deleted-pending-pvc-or-untracked-filesystem)
to check for storage that still needs attention.

## Troubleshooting

### Common Errors

* A list of possible errors that may be encountered while using the driver can be found on the
 [errors](errors.md) page.

### General Debugging

* Debugging approaches can be found on the [debug](csi-debug.md) page.
