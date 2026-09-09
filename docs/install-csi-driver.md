# Install Azure Lustre CSI driver on a kubernetes cluster

This document explains how to install Azure Lustre CSI driver on a kubernetes cluster.

## Install a released version

### Install with Helm (recommended)

Helm is the recommended installation method for production clusters. Released
charts are OCI artifacts in MCR. Set `CHART_VERSION` to the exact Helm chart
version (`A.B.C`) in the Helm README's
[released-chart table](../charts/README.md#released-chart-versions).
The table maps each chart version to its independent driver image family.
The chart's `appVersion` reports that driver release as informational metadata;
`image.tag` selects the driver image family.

```shell
CHART_VERSION=A.B.C
helm install azurelustre \
  oci://mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver \
  --namespace kube-system --create-namespace \
  --version "${CHART_VERSION}"
```

To upgrade:

> [!IMPORTANT]
> **Stop every workload using Lustre on the affected nodes before upgrading.** An
> upgrade restarts the node pods. If the release changes the Lustre client
> version, the new kernel modules can only load once the old ones are unloaded,
> and the kernel refuses to unload them while any Lustre filesystem is mounted.

```shell
CHART_VERSION=A.B.C
helm upgrade azurelustre \
  oci://mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver \
  --namespace kube-system \
  --version "${CHART_VERSION}"
```

To uninstall:

```shell
helm uninstall azurelustre -n kube-system
```

> [!IMPORTANT]
> **Migrating from a `kubectl` install to Helm.** Helm only adopts resources it
> created. If the driver was previously installed with `kubectl` /
> `install-driver.sh`, the cluster-scoped objects (the
> `azurelustre.csi.azure.com` CSIDriver and the `csi-azurelustre-*` ClusterRoles)
> have no Helm ownership metadata, so `helm install` aborts with an
> `invalid ownership metadata ... missing key "app.kubernetes.io/managed-by"`
> error. Remove the existing `kubectl` install first, then install with Helm:
>
> ```shell
> ./deploy/uninstall-driver.sh
> CHART_VERSION=A.B.C
> helm install azurelustre \
>   oci://mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver \
>   --namespace kube-system --create-namespace \
>   --version "${CHART_VERSION}"
> ```

For the full list of configurable values, version history, and advanced Helm usage, see the [Helm chart README](../charts/README.md).

### Install with kubectl

> [!IMPORTANT]
> The node plugin uses a **native sidecar** (an init container with
> `restartPolicy: Always`), which requires **Kubernetes 1.29 or later**. The
> Helm chart enforces this with `kubeVersion: '>=1.29.0-0'`; the manifests in
> `deploy/` carry no equivalent guard, so confirm the cluster version before
> applying them.

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

- Upgrade in place:

    **Stop every workload using Lustre on the affected nodes before upgrading.**
    The upgrade restarts the node pods. If the release changes the Lustre client
    version, the new kernel modules can only load once the old ones are
    unloaded, and the kernel refuses to unload them while any Lustre filesystem
    is mounted.

    Re-run `install-driver.sh` against the same cluster. The script deletes and
    recreates the controller Deployment (its `spec.selector` is immutable and
    changed in the chart restructure) and removes objects that were renamed
    (the old monolithic node DaemonSet and the un-prefixed RBAC role/binding),
    so the upgrade does not leave orphans or fail on the immutable selector.
    Deleting the controller briefly interrupts volume provisioning until the
    new controller pods become ready.

- check pods status:

    ```shell
    $ kubectl get -n kube-system pod -l app=csi-azurelustre-controller

    NAME                                         READY    STATUS    RESTARTS   AGE
    csi-azurelustre-controller-778bf84cc5-4vrth   3/3     Running   0          30s
    csi-azurelustre-controller-778bf84cc5-5zqhl   3/3     Running   0          30s

    $ kubectl get -n kube-system pod -l app=csi-azurelustre-node

    NAME                                     READY    STATUS    RESTARTS   AGE
    csi-azurelustre-node-jammy-7lw2n          4/4     Running   0          30s
    csi-azurelustre-node-noble-drlq2          4/4     Running   0          30s
    csi-azurelustre-node-azurelinux3-g6sfx    4/4     Running   0          30s
    ```

    Each node pod reports `4/4` once ready. The four containers are the
    `lustre-loader` startup sidecar (a native sidecar — an init container with
    `restartPolicy: Always` — that loads the Lustre kernel modules and brings
    up LNet), the `azurelustre` driver, the `liveness-probe` sidecar, and
    `node-driver-registrar`. See [Node pod container architecture](#node-pod-container-architecture)
    below.

## Supported node operating systems

The CSI driver runs as OS-specific node DaemonSets, each scheduled onto nodes by a `nodeSelector` or node-affinity match on the `kubernetes.azure.com/os-sku-effective` label. Only the following node OS SKUs are supported:

| `os-sku-effective` | Node DaemonSet | Image flavor |
| ------------------ | -------------- | ------------ |
| `Ubuntu2204` | `csi-azurelustre-node-jammy` | `-jammy` |
| `Ubuntu2004` | `csi-azurelustre-node-jammy` | `-jammy` |
| `Ubuntu2404` | `csi-azurelustre-node-noble` | `-noble` |
| `AzureLinux3` | `csi-azurelustre-node-azurelinux3` | `-azurelinux3` |

(`Ubuntu2004` is matched by the Jammy DaemonSet as a **deprecated** bridge for legacy CVM nodes still on Ubuntu 20.04 (focal). The entrypoint allows the jammy container on a focal host but logs that this is deprecated and will be removed in a future release.)

> [!IMPORTANT]
> A node whose `os-sku-effective` value is **not** listed above matches **no** DaemonSet, so it **silently receives no CSI driver pod** (there is no catch-all DaemonSet). Lustre volumes scheduled onto that node fail to mount with no obvious driver error -- there is simply no driver present on the node. Check a node's value with:
>
> ```shell
> kubectl get node <node-name> -o jsonpath='{.metadata.labels.kubernetes\.azure\.com/os-sku-effective}'
> ```
>
> See the [CSI Driver Troubleshooting Guide](csi-debug.md) for `os-sku-effective` troubleshooting steps.

### Karpenter and node auto-provisioning

AKS clusters with [node auto-provisioning (NAP)](https://learn.microsoft.com/azure/aks/node-auto-provisioning) enabled use managed Karpenter to add nodes dynamically based on pending pods. The AKS platform does **not** enforce OS/SKU compatibility for auto-provisioned nodes, so an unconstrained `NodePool` may create nodes with an OS SKU that matches none of the DaemonSets above -- producing nodes with no CSI driver pod and silent mount failures.

If you enable NAP, constrain every `NodePool` that can host Lustre workloads to the supported OS SKUs. Use the `kubernetes.azure.com/os-sku` and `kubernetes.io/arch` requirements (NAP evaluates all NodePools and works best when they are mutually exclusive):

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: lustre-supported
spec:
  template:
    spec:
      nodeClassRef:
        name: default            # your AKSNodeClass
      requirements:
        # Limit to OS SKUs the CSI driver supports.
        - key: kubernetes.azure.com/os-sku
          operator: In
          values: ["Ubuntu", "AzureLinux"]
        # The driver's Azure Linux image is amd64-only; pin amd64 unless you only
        # run Ubuntu Noble, the one flavor that also ships an arm64 image.
        - key: kubernetes.io/arch
          operator: In
          values: ["amd64"]
```

> [!NOTE]
> `kubernetes.azure.com/os-sku` is coarse (`Ubuntu` or `AzureLinux`); the resulting `os-sku-effective` version (`Ubuntu2204`/`Ubuntu2404`, or `AzureLinux3`) depends on your cluster's Kubernetes/AKS version. Azure Linux resolves to **Azure Linux 3** only on AKS versions where AL3 is the default -- older clusters may provision the unsupported Azure Linux 2. Confirm every provisioned node resolves to a supported `os-sku-effective` value (`Ubuntu2204`, `Ubuntu2004`, `Ubuntu2404`, or `AzureLinux3`).

## Verifying CSI Driver Readiness for Lustre Operations

Before mounting Azure Lustre filesystems, it is important to verify that the CSI driver nodes are fully initialized and ready for Lustre operations. The driver includes enhanced LNet validation that performs comprehensive readiness checks:

- Load required kernel modules (lnet, lustre)
- Configure LNet networking with valid Network Identifiers (NIDs)
- Verify LNet self-ping functionality
- Validate all network interfaces are operational
- Complete all initialization steps

### Node pod container architecture

Each `csi-azurelustre-node` pod runs four containers. Kernel-module loading and
LNet setup are split into a dedicated startup sidecar so the CSI driver socket
opens quickly: the `node-driver-registrar` no longer waits behind the full
client install, only behind the driver's much smaller userspace utils install,
which must still finish inside the registrar's hardcoded 30s connect deadline:

| Container | Kind | Responsibility | Health checks |
| --------- | ---- | -------------- | ------------- |
| `lustre-loader` | native sidecar (init container with `restartPolicy: Always`) | Installs the full Lustre client metapackage, loads the kernel modules into the shared host kernel, configures LNet, then runs an LNet-config reconcile loop for the life of the pod. | `startupProbe` + `readinessProbe`: `/app/readinessProbe.sh` (full LNet health — NIDs, self-ping, interfaces). `livenessProbe`: `test -d /sys/module/lnet` (restart only if the kernel module disappears). |
| `azurelustre` | driver | Installs the userspace Lustre tools, then serves the CSI gRPC API. | `startupProbe`: `/healthz` HTTP on port 29763 (holds off the liveness check while the utils install runs). `readinessProbe`: `test -S /csi/csi.sock` (driver socket is serving). `livenessProbe`: `/healthz` HTTP on port 29763. |
| `liveness-probe` | sidecar | Exposes the driver's `/healthz` endpoint to the kubelet. | — |
| `node-driver-registrar` | sidecar | Registers the driver socket with the kubelet. | `livenessProbe`: registration probe. |

The pod's overall `Ready` condition is the AND of every container's readiness,
including the `lustre-loader` sidecar (a native sidecar's `readinessProbe`
contributes to pod readiness). So **a node pod reports `Ready` only when LNet is
healthy *and* the driver socket is serving** — this is the signal to wait on
before mounting Lustre volumes.

> [!NOTE]
> The `lustre-loader` sidecar runs first and its `startupProbe` gates the
> `azurelustre` and `node-driver-registrar` containers from starting until LNet
> is up. On Azure Linux 3 the Lustre client install is larger than on Ubuntu, so
> a fresh node pod takes longer to reach `Ready` — this is expected, not a
> failure. Wait on the pod `Ready` condition (e.g. `kubectl wait
> --for=condition=ready`) rather than a fixed timeout or the raw container count.

### Readiness validation

The CSI driver deployment includes automated probes for accurate readiness
detection (see the table above for which container owns each probe):

#### Verification Steps

1. **Check pod readiness status:**

   ```shell
   kubectl get -n kube-system pod -l app=csi-azurelustre-node -o wide
   ```

   All node pods should show `READY` as `4/4` and `STATUS` as `Running`.

2. **Verify probe configuration:**

   ```shell
   kubectl describe -n kube-system pod -l app=csi-azurelustre-node
   ```

   Look for the `lustre-loader` sidecar's exec-based readiness/startup probes and
   the `azurelustre` driver's socket readiness probe. A freshly created pod may
   show startup probe failures while LNet comes up and while the driver installs
   its utils; on a pod that has settled, no recent probe failures should appear
   in the Events section.

3. **Monitor LNet validation logs (loader sidecar):**

   ```shell
   kubectl logs -n kube-system -l app=csi-azurelustre-node -c lustre-loader --tail=20
   ```

   Look for `LNet is loaded` and reconcile-loop messages indicating LNet
   initialization is complete.

4. **Monitor driver logs:**

   ```shell
   kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre --tail=20
   ```

   Look for `Listening for connections` and successful GRPC operation logs
   indicating the driver socket is serving.

> **Note**: If you encounter readiness or initialization issues, see the [CSI Driver Troubleshooting Guide](csi-debug.md#lnet-readiness-troubleshooting-loader-sidecar) for detailed debugging steps.

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

## Node pool taints and tolerations

The node DaemonSets and the controller carry a deliberate, minimal set of tolerations so the driver lands where Lustre workloads run and stays off AKS-managed reserved capacity:

- **The node plugin does *not* tolerate the `CriticalAddonsOnly` taint.** This is the AKS-recommended way to keep a DaemonSet off system node pools (see [Manage system node pools](https://learn.microsoft.com/azure/aks/use-system-pools)). It also keeps the plugin off **AKS Automatic** system nodes, which are tainted `CriticalAddonsOnly`. On a cluster whose only pool is an *untainted* system pool, the plugin still runs there so Lustre volumes can mount.
- **The node plugin tolerates the common AKS user-pool taints by default** — spot (`kubernetes.azure.com/scalesetpriority=spot:NoSchedule`) and GPU (`sku=gpu:NoSchedule` and `nvidia.com/gpu:NoSchedule`) — so it runs on the pre-emptible/accelerated pools where Lustre HPC/AI workloads typically schedule.
- **The controller** tolerates only control-plane taints. It intentionally does not chase spot/GPU nodes and relies on an untainted node being available (AKS Standard keeps an untainted system pool by default; on AKS Automatic, node auto-provisioning creates an untainted node for the pending controller).

> [!IMPORTANT]
> If you run Lustre workloads on a **custom-tainted** user node pool (any taint other than the spot/GPU taints above), the node plugin will **not** schedule there and pods on that pool fail to mount Lustre volumes with no obvious driver error — the same silent-failure signature as an unsupported `os-sku-effective` node above. Add a matching toleration via the Helm value `node.tolerations` (which **replaces** the default set, so include the spot/GPU entries you still need). For the static `deploy/*.yaml` manifests, edit the `tolerations` block in each `csi-azurelustre-node-*.yaml`.

## Custom Entrypoint (Advanced)

The CSI driver supports overriding the built-in entrypoint script via a Kubernetes ConfigMap. This is intended as a **troubleshooting/debugging feature** for use when suggested by Microsoft support, or for customers with custom initialization requirements (e.g., non-standard networking setups).

### How It Works

Each container runs the same image through a wrapper script (`start.sh`) that checks for a custom entrypoint at `/app/custom-entrypoint/entrypoint.sh`. If found, it execs the custom version; otherwise it falls back to the built-in `/app/entrypoint.sh`. The custom entrypoint is mounted from an optional ConfigMap (`csi-azurelustre-entrypoint`) into the **node DaemonSet pods only** — the controller deployment is not affected.

Because the node plugin is split into a `lustre-loader` startup sidecar and an `azurelustre` driver container (see [Node pod container architecture](#node-pod-container-architecture)), the ConfigMap is mounted into **both** of those containers, and the **same** custom script is therefore executed by both. The script must branch on the `AZURELUSTRE_CSI_ROLE` environment variable that the pod sets per container so that each container does the right work:

| `AZURELUSTRE_CSI_ROLE` | Container | The custom script must |
| --- | --- | --- |
| `loader` | `lustre-loader` sidecar | Install the full Lustre client (kmod + kernel + utils), load the kernel modules into the shared host kernel, configure LNet, then keep running for the life of the pod (e.g. an LNet reconcile loop). It must **not** exec the CSI driver binary. |
| `driver` | `azurelustre` | Install only the userspace Lustre utils, then `exec "$@"` to launch the CSI driver binary passed by `start.sh`. |
| `controller` | controller deployment | Just `exec "$@"` — no kernel-module work. (The controller does not mount the ConfigMap, so an override never reaches it; the built-in entrypoint handles this role.) |

### Installing with a Custom Entrypoint

#### kubectl

Pass `--custom-entrypoint <file>` to the install script:

```shell
./deploy/install-driver.sh --custom-entrypoint ./my-entrypoint.sh local
```

This creates a ConfigMap from the provided file and restarts the node DaemonSet pods to use it.

#### Helm

The Helm chart always mounts the custom entrypoint ConfigMap as optional, so no chart upgrade is needed. Create the ConfigMap and restart the node pods:

```shell
# Create the ConfigMap from your custom entrypoint script
kubectl create configmap csi-azurelustre-entrypoint \
  --from-file=entrypoint.sh=./my-entrypoint.sh \
  -n kube-system

# Restart node DaemonSet pods to pick up the custom entrypoint
kubectl rollout restart daemonset -l app=csi-azurelustre-node -n kube-system
```

### Reverting to the Built-in Entrypoint

#### Revert with kubectl

Run the install script without the `--custom-entrypoint` flag:

```shell
./deploy/install-driver.sh local
```

This deletes the ConfigMap and restarts the node pods to use the built-in entrypoint. **The custom entrypoint is not sticky** — each install must explicitly request it.

#### Revert with Helm

Delete the ConfigMap and restart the node pods:

```shell
kubectl delete configmap csi-azurelustre-entrypoint -n kube-system --ignore-not-found
kubectl rollout restart daemonset -l app=csi-azurelustre-node -n kube-system
```

### Important Notes

- The custom entrypoint replaces the **entire** built-in entrypoint, including Lustre client installation logic, and is used by **both** the `lustre-loader` sidecar and the `azurelustre` driver container. Your custom script is responsible for all per-role setup (see the role table under [How It Works](#how-it-works)) — including loading the kernel modules and bringing up LNet in the `loader` role — and, in the `driver` role, for launching the CSI driver binary.
- A good starting point for a custom entrypoint is the built-in script at `pkg/azurelustreplugin/entrypoint.sh`, which already dispatches on `AZURELUSTRE_CSI_ROLE`. Copy and adapt it rather than writing a single-flow script, so that both the `loader` and `driver` roles are handled.
- The `lustre-loader` sidecar's readiness is gated by `/app/readinessProbe.sh` (an LNet health check) that the kubelet runs **directly** — it is **not** overridable by the custom entrypoint. A `loader` custom script that does not bring LNet up the way the probe expects will fail the sidecar's `startupProbe`, which keeps the `azurelustre` and `node-driver-registrar` containers from starting and the node pod from ever reaching `Ready`.
- **Migration note:** earlier driver versions selected behavior with `AZURELUSTRE_CSI_INSTALL_LUSTRE_CLIENT` (`yes`/`no`); this has been replaced by `AZURELUSTRE_CSI_ROLE` (`loader`/`driver`/`controller`). A custom entrypoint carried over from before the sidecar split must be updated to read `AZURELUSTRE_CSI_ROLE` and implement the `loader` and `driver` roles separately.
- **Security note:** the custom entrypoint is stored in the `csi-azurelustre-entrypoint` ConfigMap in `kube-system` and is executed by a privileged container. Treat this as a code-injection path: tightly restrict RBAC for creating or updating this ConfigMap, and only use custom entrypoints in trusted/admin scenarios.
- If you edit the ConfigMap directly (e.g., `kubectl edit configmap csi-azurelustre-entrypoint -n kube-system`), you must manually restart the node DaemonSets for changes to take effect: `kubectl rollout restart daemonset csi-azurelustre-node-jammy csi-azurelustre-node-noble csi-azurelustre-node-azurelinux3 -n kube-system`
- The uninstall script automatically cleans up the ConfigMap if it exists.
