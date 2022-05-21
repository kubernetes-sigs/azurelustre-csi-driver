# Static Provisioning

This document explains how to deploy your workload with an existing Azure Lustre cluster using Lustre CSI driver.

&nbsp;

## Create a Volume bound to an existing Azure Lustre Cluster

### Option 1: Use Storage Class

* Download
[static provision storage class](./examples/storageclass_existing_lustre.yaml)
and [PVC file](./examples/pvc_storageclass.yaml).

* Edit the `EXISTING_LUSTRE_FS_NAME` and `EXISTING_LUSTRE_IP_ADDRESS` in
storage class.

* Create storage class and `PVC`

```shell
kubectl create -f storageclass_existing_lustre.yaml
kubectl create -f pvc_storageclass.yaml
```

&nbsp;

### Option 2: Use PV

* Download [PV file](./examples/pv.yaml) and
[PVC file](./examples/pvc_pv.yaml)

* Edit the `EXISTING_LUSTRE_FS_NAME`, `EXISTING_LUSTRE_IP_ADDRESS` and
`UNIQUE_IDENTIFIER_VOLUME_ID` in pv.

* Create PV and PVC.

```shell
kubectl create -f pv.yaml
kubectl create -f pvc_pv.yaml
```

&nbsp;

## Use the Volume

* Make sure pvc is created and in Bound status after a while

```shell
kubectl describe pvc pvc-lustre
```

* Download [demo pod echo date](./examples/pod_echo_date.yaml)

* Create the POD with PVC mount

```shell
kubectl create -f pod_echo_date.yaml
```

* Execute `df -h` command in the container and you can see the volume is
mounted

```shell
$ kubectl exec -it lustre-echo-date -- df -h

Filesystem                Size      Used     Available Use% Mounted on
...
${ip}@tcp:/${fs}          976.6G    154.7G    821.9G   16%  /mnt/lustre
...
```
