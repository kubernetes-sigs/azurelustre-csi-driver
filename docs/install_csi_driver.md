# Install Azure Managed Lustre CSI on a kubernetes cluster

This document explains how to install Azure Managed Lustre CSI on a kubernetes
cluster.

## Install with kubectl

* install

```shell
git clone https://github.com/jusjin-org/amlfs-csi-driver.git
cd amlfs-csi-driver
./deploy/install-driver.sh
```

* check pods status:

```shell
$ kubectl get -n kube-system pod -l app=csi-amlfs-controller

NAME                                    READY   STATUS    RESTARTS   AGE
csi-amlfs-controller-778bf84cc5-4vrth   3/3     Running   0          30s
csi-amlfs-controller-778bf84cc5-5zqhl   3/3     Running   0          30s

$ kubectl get -n kube-system pod -l app=csi-amlfs-node

NAME                   READY   STATUS    RESTARTS   AGE
csi-amlfs-node-7lw2n   3/3     Running   0          30s
csi-amlfs-node-drlq2   3/3     Running   0          30s
csi-amlfs-node-g6sfx   3/3     Running   0          30s
```
