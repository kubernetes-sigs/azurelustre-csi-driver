# Install Azure Lustre CSI driver on a kubernetes cluster

This document explains how to install Azure Lustre CSI driver on a kubernetes cluster.

## Install with kubectl

- Option 1: Remote install

    ```shell
    curl -skSL https://raw.githubusercontent.com/kubernetes-sigs/azurelustre-csi-driver/master/deploy/install-driver.sh | bash -s master --
    ```

- Option 2: Local install

    ```shell
    git clone https://github.com/kubernetes-sigs/azurelustre-csi-driver.git
    cd azurelustre-csi-driver
    ./deploy/install-driver.sh
    ```

- check pods status:

    ```shell
    $ kubectl get -n kube-system pod -l app=csi-azurelustre-controller

    NAME                                         READY    STATUS    RESTARTS   AGE
    csi-azurelustre-controller-778bf84cc5-4vrth   3/3     Running   0          30s
    csi-azurelustre-controller-778bf84cc5-5zqhl   3/3     Running   0          30s

    $ kubectl get -n kube-system pod -l app=csi-azurelustre-node

    NAME                        READY    STATUS    RESTARTS   AGE
    csi-azurelustre-node-7lw2n   3/3     Running   0          30s
    csi-azurelustre-node-drlq2   3/3     Running   0          30s
    csi-azurelustre-node-g6sfx   3/3     Running   0          30s
    ```
