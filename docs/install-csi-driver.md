# Install Azure Lustre CSI driver on a kubernetes cluster

This document explains how to install Azure Lustre CSI driver on a kubernetes cluster.

## Specific instructions for Dynamic Provisioning Branch

### Install with kubectl (dynamic provisioning - public preview)

> **Note**: Dynamic provisioning functionality is currently in public preview. This preview version is provided without a service level agreement and is not recommended for production workloads. Some features may not be supported or may have constrained capabilities.

- Option 1: Remote install

    ```shell
    curl -skSL https://raw.githubusercontent.com/kubernetes-sigs/azurelustre-csi-driver/main/deploy/install-driver.sh | bash -s dynamic-provisioning-preview
    ```

- Option 2: Local install

    ```shell
    git clone https://github.com/kubernetes-sigs/azurelustre-csi-driver.git
    cd azurelustre-csi-driver
    ./deploy/install-driver.sh dynamic-provisioning-preview
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

### Verifying CSI Driver Readiness for Lustre Operations

Before mounting Azure Lustre filesystems, it's important to verify that the CSI driver nodes are fully initialized and ready for Lustre operations. The driver includes **enhanced LNet validation** that performs comprehensive readiness checks:

- Load required kernel modules (lnet, lustre)
- Configure LNet networking with valid Network Identifiers (NIDs)
- Verify LNet self-ping functionality
- Validate all network interfaces are operational
- Complete all initialization steps

#### Enhanced Readiness Validation

The CSI driver now provides **exec-based readiness probes** for accurate readiness detection:

- **Readiness & Startup Probes**: `/app/readinessProbe.sh` - Direct validation with comprehensive LNet checking
- **HTTP Endpoint**: `/healthz` (Port 29763) - Available for manual testing and liveness monitoring

#### Verification Commands

1. **Check pod readiness status:**
   ```shell
   kubectl get -n kube-system pod -l app=csi-azurelustre-node -o wide
   ```
   All node pods should show `READY` status as `3/3` and `STATUS` as `Running`.

2. **Test enhanced readiness endpoint directly:**
   ```shell
   kubectl exec -n kube-system <pod-name> -c azurelustre -- curl -s localhost:29763/healthz
   ```
   Should return `ok` (HTTP 200) when LNet validation passes, or `not ready` (HTTP 503) if any validation fails.

3. **Test liveness endpoint:**
   ```shell
   kubectl exec -n kube-system <pod-name> -c azurelustre -- curl -s localhost:29763/livez
   ```
   Should return `alive` (HTTP 200) indicating basic container health.

4. **Check detailed probe status:**
   ```shell
   kubectl describe -n kube-system pod -l app=csi-azurelustre-node
   ```
   Look for successful readiness and liveness probe checks in the Events section.

5. **Review enhanced validation logs:**
   ```shell
   kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre --tail=20
   ```
   Look for enhanced LNet validation messages:
   - `"LNet validation passed: all checks successful"`
   - `"Found NIDs: <network-identifiers>"`
   - `"LNet self-ping to <nid> successful"`
   - `"All LNet interfaces operational"`

#### Troubleshooting Failed Readiness

If the readiness probe fails (exit code 1), check the logs for specific validation failure reasons:

```shell
# Check for detailed validation failure reasons
kubectl logs -n kube-system <pod-name> -c azurelustre | grep -E "(LNet validation failed|Failed to|not operational)"
```

Common issues and solutions:
- **"No valid NIDs"**: LNet networking not properly configured
- **"Self-ping test failed"**: Network connectivity issues
- **"Interfaces not operational"**: Network interfaces not in UP state
- **"Lustre module not loaded"**: Kernel module loading issues

**Important**: The enhanced validation ensures the driver reports ready only when LNet is fully functional for Lustre operations. Wait for all CSI driver node pods to pass enhanced readiness checks before creating PersistentVolumes or mounting Lustre filesystems.

## Default instructions for production release

### Install with kubectl (current production release)

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


### Verifying CSI Driver Readiness for Lustre Operations

Before mounting Azure Lustre filesystems, it is important to verify that the CSI driver nodes are fully initialized and ready for Lustre operations. The driver includes **enhanced LNet validation** that performs comprehensive readiness checks:

- Load required kernel modules (lnet, lustre)
- Configure LNet networking with valid Network Identifiers (NIDs)
- Verify LNet self-ping functionality
- Validate all network interfaces are operational
- Complete all initialization steps

#### Enhanced Readiness Validation

The CSI driver now provides **HTTP health endpoints** for accurate readiness detection:

- **`/healthz`** (Port 29763): Enhanced readiness check with comprehensive LNet validation
- **`/livez`** (Port 29763): Basic liveness check to prevent unnecessary restarts

#### Verification Commands

1. **Check pod readiness status:**
   ```shell
   kubectl get -n kube-system pod -l app=csi-azurelustre-node -o wide
   ```
   All node pods should show `READY` status as `3/3` and `STATUS` as `Running`.

2. **Test enhanced readiness endpoint directly:**
   ```shell
   kubectl exec -n kube-system <pod-name> -c azurelustre -- curl -s localhost:29763/healthz
   ```
   Should return `ok` (HTTP 200) when LNet validation passes, or `not ready` (HTTP 503) if any validation fails.

3. **Test liveness endpoint:**
   ```shell
   kubectl exec -n kube-system <pod-name> -c azurelustre -- curl -s localhost:29763/livez
   ```
   Should return `alive` (HTTP 200) indicating basic container health.

4. **Check detailed probe status:**
   ```shell
   kubectl describe -n kube-system pod -l app=csi-azurelustre-node
   ```
   Look for successful readiness and liveness probe checks in the Events section.

5. **Review enhanced validation logs:**
   ```shell
   kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre --tail=20
   ```
   Look for enhanced LNet validation messages:
   - `"LNet validation passed: all checks successful"`
   - `"Found NIDs: <network-identifiers>"`
   - `"LNet self-ping to <nid> successful"`
   - `"All LNet interfaces operational"`

#### Troubleshooting Failed Readiness

If the readiness endpoint returns `not ready`, check the logs for specific validation failure reasons:

```shell
# Check for detailed validation failure reasons
kubectl logs -n kube-system <pod-name> -c azurelustre | grep -E "(LNet validation failed|Failed to|not operational)"
```

Common issues and solutions:
- **"No valid NIDs"**: LNet networking not properly configured
- **"Self-ping test failed"**: Network connectivity issues
- **"Interfaces not operational"**: Network interfaces not in UP state
- **"Lustre module not loaded"**: Kernel module loading issues

**Important**: The enhanced validation ensures the driver reports ready only when LNet is fully functional for Lustre operations. Wait for all CSI driver node pods to pass enhanced readiness checks before creating PersistentVolumes or mounting Lustre filesystems.

