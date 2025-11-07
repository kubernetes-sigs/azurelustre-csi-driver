# Install Azure Lustre CSI driver on a kubernetes cluster

This document explains how to install Azure Lustre CSI driver on a kubernetes cluster.

## Instructions for current production release

### Install with kubectl

- Option 1: Remote install

    ```shell
    curl -skSL https://raw.githubusercontent.com/kubernetes-sigs/azurelustre-csi-driver/main/deploy/install-driver.sh | bash -s main
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

## Verifying CSI Driver Readiness for Lustre Operations

Before mounting Azure Lustre filesystems, it is important to verify that the CSI driver nodes are fully initialized and ready for Lustre operations. The driver includes enhanced LNet validation that performs comprehensive readiness checks:

- Load required kernel modules (lnet, lustre)
- Configure LNet networking with valid Network Identifiers (NIDs)
- Verify LNet self-ping functionality
- Validate all network interfaces are operational
- Complete all initialization steps

### Enhanced Readiness Validation

The CSI driver deployment includes automated **exec-based readiness probes** for accurate readiness detection:

- **Readiness & Startup Probes**: `/app/readinessProbe.sh` - Exec-based validation with comprehensive LNet checking
- **Liveness Probe**: `/healthz` (Port 29763) - HTTP endpoint for basic container health

#### Verification Steps

1. **Check pod readiness status:**

   ```shell
   kubectl get -n kube-system pod -l app=csi-azurelustre-node -o wide
   ```

   All node pods should show `READY` status as `3/3` and `STATUS` as `Running`.

2. **Verify probe configuration:**

   ```shell
   kubectl describe -n kube-system pod -l app=csi-azurelustre-node
   ```

   Look for exec-based readiness and startup probe configuration and check that no recent probe failures appear in the Events section.

3. **Monitor validation logs:**

   ```shell
   kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre --tail=20
   ```

   Look for CSI driver startup and successful GRPC operation logs indicating driver initialization is complete.

> **Note**: If you encounter readiness or initialization issues, see the [CSI Driver Troubleshooting Guide](csi-debug.md#enhanced-lnet-validation-troubleshooting) for detailed debugging steps.

**Important**: The enhanced validation ensures the driver reports ready only when LNet is fully functional for Lustre operations. Wait for all CSI driver node pods to pass enhanced readiness checks before creating PersistentVolumes or mounting Lustre filesystems.

## Startup Taints

When the CSI driver starts on each node, it automatically removes the following taint if present:

- **Taint Key**: `azurelustre.csi.azure.com/agent-not-ready`
- **Taint Effect**: `NoSchedule`

This ensures that:

1. **Node Readiness**: Pods requiring Azure Lustre storage are only scheduled to nodes where the CSI driver is fully initialized
2. **Lustre Client Ready**: The node has successfully loaded Lustre kernel modules and networking components

### Configuring Startup Taint Behavior

The startup taint functionality is enabled by default but can be configured during installation:

- **Default Behavior**: Startup taint removal is **enabled** by default
- **Disable Taint Removal**: To disable, set `--remove-not-ready-taint=false` in the driver deployment

For most AKS users, the default behavior provides optimal pod scheduling and should not be changed
