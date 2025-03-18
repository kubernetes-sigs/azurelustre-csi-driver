# Dynamic Provisioning

This document explains how to deploy your workload to a dynamically provisioned Azure Managed Lustre cluster
using the Lustre CSI driver.

&nbsp;

## Create an Azure Managed Lustre cluster bound to a Persistent Volume Claim

### Use the Storage Class

* Download
[dynamic provision storage class](./examples/storageclass_dynprov_lustre.yaml)
and [persistent volume claim file](./examples/pvc_storageclass_dynprov.yaml).

> [!IMPORTANT]
***IMPORTANT NOTE***: The example storage class is configured so that Azure Managed Lustre clusters
will be automatically deleted when the persistent volume claim that creates them is deleted. If you
require the Azure Managed Lustre cluster to remain after the persistent volume claim is deleted, set
the `reclaimPolicy` in the storage class to `Retain` instead of `Delete`.

* Edit the `SKU_NAME`, `ZONE`, `MAINTENANCE_DAY`, and `MAINTENANCE_TIME_OF_DAY` in the storage class.
  * By default, the Azure Managed Lustre cluster will be created in the same location, resource group, and subnet
  as the Azure Kubernetes Service cluster that is running this driver. You can choose an existing location,
  resource group, and subnet for the cluster to be deployed into by setting the `LOCATION`, `RESOURCE_GROUP_NAME`,
  `EXISTING_VNET_RG`, `EXISTING_VNET_NAME`, and/or `EXISTING_SUBNET_NAME` values in the storage class.
  * You can optionally set user-assigned identities, tags, and/or the subdirectory template for
  pods to use by setting the `IDENTITIES`, `TAGS`, and/or the `SUBDIRECTORY` values in the storage class.

* Create the storage class and persistent volume claim:

```shell
kubectl create -f storageclass_dynprov_lustre.yaml
kubectl create -f pvc_storageclass_dynprov.yaml
```

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

> [!IMPORTANT]
This example storage class contains a `ResourceQuota` definition for testing purposes. This limits the
number of PVCs that can be created for this storage class to a single volume. This `ResourceQuota`
is optional, but it is recommended to set a quota to prevent the accidental creation of too many
Azure Managed Lustre clusters, especially while learning or testing. This limit applies only to
this storage class, so Azure Managed Lustre clusters could still be created using additional
storage classes. Note that there may a separate quota limit in your Azure subscription for the
total number of Azure Managed Lustre clusters as well, as described below.

* A single storage class can be used to create multiple persistent volume claims. Each new pvc will
create a new Azure Managed Lustre cluster to back the new volume. If multiple cluster required with
different configurations, any number of storage classes can be created, each for a different
configuration for the cluster. This way, it is possible to make various clusters based on different
SKUs, in different subnets, etc.
  * Note that there may be a subscription-wide limit to the number of Azure Managed Lustre clusters
  that a given identity can create. If more volumes are created than clusters that can be created,
  those persistent volume claims will remain in a `Pending` status and report this limitation in the
  events list until enough other clusters have been deleted or the subscription-wide quota has been
  increased.

* Wait for the Azure Managed Lustre cluster to be created, which may take ten minutes or more.
During this time, you can check the progress of the persistent volume claim.

```shell
kubectl describe pvc pvc-lustre-dynprov
```

* While it is creating, the `Status` will be `Pending` and you will see a message such as the
following in the pvc events list:
`Waiting for a volume to be created either by the external provisioner 'azurelustre.csi.azure.com'…`

* Once the cluster is ready, it will have a `Bound` status, and a message will appear in the event
list such as `Successfully provisioned volume pvc-78876f95-32c2-41c4-bdfa-eb92d1eeb341`
  * Any issues encountered during creation should appear in the events list for the pvc. If the issue
  is not obvious from the event message, check the controller pod logs for any further relevant
  messages. In some cases, the cluster creation must be retried by the driver, which will
  significantly increase the volume creation time. This should be clear from the volume claim event
  list and pod logs, and should eventually succeed. Otherwise, any terminal errors will be reported
  to the user.

&nbsp;

## Use the Volume

* Download [dynamic provisioning demo pod echo date](./examples/pod_echo_date_dynprov.yaml)

* Create the POD with PVC mount

```shell
kubectl create -f pod_echo_date_dynprov.yaml
```

* Execute `df -h` command in the container and you can see the volume is
mounted

```shell
$ kubectl exec -it lustre-echo-date-dynprov -- df -h

Filesystem                Size      Used     Available Use% Mounted on
...
${ip}@tcp:/${fs}          976.6G    154.7G    821.9G   16%  /mnt/lustre
...
```

&nbsp;

## Delete the Volume

* Delete the persistent volume claim. If you had the storage class's `reclaimPolicy` set to `Delete`,
this will also delete the persistent volume and Azure Managed Lustre cluster that was created for the
volume. If you had the `reclaimPolicy` set to `Retain`, you will have to delete the Azure Managed
Lustre cluster manually when you no longer need it.

```shell
kubectl delete pvc pvc-lustre-dynprov
```

* Wait for the Azure Managed Lustre cluster to be deleted, which may take ten minutes or more. During
this time, you can check the progress of the persistent volume (the persistent volume claim will be
deleted at this point):

```shell
kubectl describe pv <unique-volume-name-from-pvc>
```

* Any issues encountered during creation should appear in the events list for the persistent volume.
If the issue is not obvious from the event message, check the controller pod logs for any further
relevant messages.
