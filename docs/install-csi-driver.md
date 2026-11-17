# Install Azure Lustre CSI driver on a kubernetes cluster

This document explains how to install Azure Lustre CSI driver on a kubernetes cluster.

## Install a released version

### Install with Helm (recommended)

Helm is the recommended installation method for production clusters. Released
charts are OCI artifacts in MCR. Set `CHART_VERSION` to the exact Helm chart
version (for example: `0.7.0`) in the Helm README's
[released-chart table](../charts/README.md#released-chart-versions).
The table maps each chart version to its independent driver image family.
The chart's `appVersion` reports that driver release as informational metadata;
`image.tag` selects the driver image family.

```shell
CHART_VERSION=0.7.0
helm install azurelustre --wait \
  oci://mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver \
  --namespace kube-system --create-namespace \
  --version "${CHART_VERSION}"
```

To upgrade:

> [!IMPORTANT]
> The development chart defaults to rolling node-pod updates. Compatible driver
> updates can retain supported resident modules without draining workloads.
> Confirm the running loader preserves modules on termination and the target
> release supports those clients. Older module-unloading loaders, unqualified
> custom entrypoints, and incompatible transitions require planned maintenance.
> Check the chosen release's defaults and the [upgrade
> precautions](../charts/README.md#upgrade), especially when switching a staged
> `OnDelete` installation to `RollingUpdate`. Pod replacement alone does not
> guarantee client-module activation.

```shell
CHART_VERSION=0.7.0
helm upgrade azurelustre --wait \
  oci://mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver \
  --namespace kube-system \
  --version "${CHART_VERSION}"
```

Before uninstalling, follow the [safe teardown
procedure](../charts/README.md#safe-teardown-procedure): stop consumers and
provisioning, decide which data must survive, and complete volume cleanup while
the driver is still running. Deleting a dynamically provisioned PVC with a
`Delete` reclaim policy can delete its backing AMLFS filesystem and data.

```shell
helm uninstall azurelustre -n kube-system
```

See [guard requirements](../charts/README.md#scope-and-prerequisites)
and [blocked-uninstall recovery](../charts/README.md#troubleshoot-a-blocked-uninstall).

> [!IMPORTANT]
> **Migrating from a `kubectl` install to Helm.** Helm only adopts resources it
> created. If the driver was previously installed with `kubectl` /
> `install-driver.sh`, the cluster-scoped objects (the
> `azurelustre.csi.azure.com` CSIDriver and the `csi-azurelustre-*` ClusterRoles)
> have no Helm ownership metadata, so `helm install` aborts with an
> `invalid ownership metadata ... missing key "app.kubernetes.io/managed-by"`
> error. Complete the [safe teardown procedure](../charts/README.md#safe-teardown-procedure),
> then remove the existing `kubectl` install before installing with Helm.
> Unmounting alone does not satisfy the new guard while PVs remain.
>
> ```shell
> ./deploy/uninstall-driver.sh
> CHART_VERSION=0.7.0
> helm install azurelustre --wait \
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

> [!NOTE]
> Both `install-driver.sh` and `uninstall-driver.sh` install into `kube-system`
> by default. To use a different namespace, set the `NAMESPACE` environment
> variable (the script creates the namespace if it does not exist and rewrites
> the manifests' namespace at apply time):
>
> ```shell
> NAMESPACE=azurelustre-system ./deploy/install-driver.sh local
> ```
>
> Caveats when using a non-default namespace:
>
> - **Use the same `NAMESPACE` to uninstall.** `uninstall-driver.sh` also
>   defaults to `kube-system`; run it with the same value you installed with, or
>   it will refuse (to avoid deleting the shared cluster-scoped CSIDriver/RBAC
>   that a live install depends on).
> - **One install per cluster.** The CSIDriver object and ClusterRoles are
>   cluster-global singletons, so switching namespaces is a move, not an
>   in-place migration: uninstall the existing namespace first (`install-driver.sh`
>   refuses if the driver is already installed in another namespace).
>   The scripts search controller Deployments, node DaemonSets, and Pods across
>   **all namespaces**, using either standard driver label
>   (`app.kubernetes.io/name` or `app.kubernetes.io/part-of`) and legacy `app`
>   labels. This also protects partial installs with no controller.
>   Install checks the cluster-scoped `azurelustre.csi.azure.com` CSIDriver by
>   name; if it exists without matching workloads, inspect and uninstall the
>   leftovers before reinstalling, since its namespace cannot be determined.
>   Discovery errors abort before any changes. Uninstall refuses to delete
>   shared resources whenever matching workloads remain in another namespace,
>   even if the target namespace also contains a controller. `FORCE=1` overrides
>   that conflict check for deliberate cleanup, but not discovery errors.
> - **Privileged/critical scheduling.** The pods use the `system-node-critical` /
>   `system-cluster-critical` priority classes. Clusters that restrict those
>   classes to `kube-system` (via a `ResourceQuota` scopeSelector) will reject the
>   pods in another namespace.
> - If you use workload identity, the federated credential subject must match the
>   namespace — see [workload-identity.md](./workload-identity.md).

- Upgrade in place:

    Current source manifests use `RollingUpdate`. The installer waits for
    updated, Ready, available node pods, including completion of each flavor's
    rollout. Compatible updates can preserve supported resident modules and
    application mounts; follow the [upgrade precautions](../charts/README.md#upgrade)
    before changing a staged strategy or using an unqualified client combination.
    Pod replacement alone does not activate newer modules.

    **For migration from the old monolithic DaemonSet or a module-unloading
    loader, stop affected workloads and wait for Lustre unmounts first.** Keep
    existing PVCs and PVs. Custom-entrypoint changes also require the precautions
    in [Custom Entrypoint](#custom-entrypoint-advanced).

    Re-run `install-driver.sh` against the same cluster. The script deletes and
    recreates the controller Deployment (its `spec.selector` is immutable and
    changed in the chart restructure) and removes objects that were renamed
    (the old monolithic node DaemonSet and the un-prefixed RBAC role/binding),
    so the upgrade does not leave orphans or fail on the immutable selector.
    Deleting the controller briefly interrupts volume provisioning until the
    new controller pods become ready.

  - Uninstall:

    First follow the [safe teardown procedure](../charts/README.md#safe-teardown-procedure),
    including data-retention decisions and completion of unmount/backend cleanup.
    Then run the uninstall script from the matching driver checkout:

    ```shell
    ./deploy/uninstall-driver.sh
    ```

    The script blocks removal while matching PVs exist or its API query fails.
    It cannot detect provisioning before a PV exists.
    See [emergency bypasses](../charts/README.md#emergency-bypasses) for the
    distinct meanings and risks of the script's `--force`, Helm's `--no-hooks`,
    and Azure extension deletion.

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

Each `csi-azurelustre-node` pod runs four containers. Current source manifests
start CSI independently of loader and driver package installation so ordinary
unmounts remain available during package recovery. The driver installs its
userspace tools in a retrying worker and gates new mounts on fresh, compatible
host-runtime evidence. An HTTP startup probe protects registrar initialization.
The registrar still exits if its initial CSI connection times out after 30 seconds;
Kubernetes then restarts it with backoff.

| Container | Kind | Responsibility | Health checks |
| --------- | ---- | -------------- | ------------- |
| `lustre-loader` | native sidecar (init container with `restartPolicy: Always`) | Installs the full client, loads resident modules if absent, configures LNet, and reconciles it for the life of the pod. Routine termination preserves modules. | No blocking startup probe. `readinessProbe`: `/app/readinessProbe.sh`. `livenessProbe`: `/app/startupState.sh live`, allowing ten minutes of setup, then checking that LNet remains loaded. |
| `azurelustre` | driver | Starts CSI immediately; installs userspace tools in a retrying worker. New mounts require fresh supported host runtime. | `startupProbe` and `livenessProbe`: `/healthz` on port 29763. `readinessProbe`: CSI socket plus `/app/readinessProbe.sh`, including current runtime observation and LNet health. |
| `liveness-probe` | sidecar | Exposes the driver's `/healthz` endpoint to the kubelet. | — |
| `node-driver-registrar` | sidecar | Registers the driver socket with the kubelet. | `startupProbe` + `livenessProbe`: `/healthz` HTTP on port 29764 (registration socket responds; not confirmation of completed kubelet registration). |

The pod's overall `Ready` condition is the AND of every container's readiness,
including the `lustre-loader` sidecar (a native sidecar's `readinessProbe`
contributes to pod readiness). Wait for Pod readiness during installation and
rollouts. A later loader restart can leave the Pod NotReady while the driver
still accepts new mounts against fresh, healthy, supported resident modules.
CSI cleanup and already-published retries do not depend on runtime readiness.

> [!NOTE]
> `node.cleanupCapableStartup=false` explicitly restores legacy Helm startup:
> loader readiness gates the main containers, and driver utilities install
> before CSI opens. Package failures in that mode can block cleanup. The current
> source defaults require a matching driver image and a valid flavor policy;
> they do not activate newer modules merely by replacing the CSI pod.

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

   Look for loader readiness and startup-state liveness checks, and driver
   socket-plus-runtime readiness. Package installation can leave readiness
   failing while CSI liveness succeeds. Check runtime evidence rather than
   treating socket existence as permission to mount.

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
| `loader` | `lustre-loader` sidecar | Preserve the built-in startup-state handoff; install the full client, load missing modules, configure and reconcile LNet. Do not exec CSI or unload resident modules on routine termination. |
| `driver` | `azurelustre` | With cleanup-capable startup, launch CSI promptly with `exec "$@"`; its worker invokes the bundled userspace installer. Legacy mode installs utilities before exec. |
| `controller` | controller deployment | Just `exec "$@"` — no kernel-module work. (The controller does not mount the ConfigMap, so an override never reaches it; the built-in entrypoint handles this role.) |

### Installing with a Custom Entrypoint

#### kubectl

Pass `--custom-entrypoint <file>` to the install script:

```shell
./deploy/install-driver.sh --custom-entrypoint ./my-entrypoint.sh local
```

This creates or updates the ConfigMap. ConfigMap-only changes do not restart
node pods, even with `RollingUpdate`; new or restarted containers read the
current configuration. The installer also applies the DaemonSet templates, so
template changes can roll pods immediately. Cordon and drain affected nodes
before applying an arbitrary custom entrypoint, and verify no Lustre mounts remain.

#### Helm

The Helm chart always mounts the custom entrypoint ConfigMap as optional, so no
chart upgrade is needed. Create the ConfigMap, then replace node pods only after
cordoning and draining their nodes and verifying that no Lustre mounts remain:

```shell
# Create the ConfigMap from your custom entrypoint script
kubectl create configmap csi-azurelustre-entrypoint \
  --from-file=entrypoint.sh=./my-entrypoint.sh \
  -n kube-system

# After safely draining one node, delete only that node's CSI pod.
kubectl delete pod -n kube-system <drained-node-csi-pod>
kubectl wait -n kube-system --for=condition=Ready \
  pod/<replacement-node-csi-pod> --timeout=10m
kubectl uncordon <node-name>
```

### Reverting to the Built-in Entrypoint

#### Revert with kubectl

Run the install script without the `--custom-entrypoint` flag:

```shell
./deploy/install-driver.sh local
```

This deletes the ConfigMap but does not restart pods solely for that deletion.
New or restarted containers use the built-in entrypoint. DaemonSet template
changes can still roll pods, so drain affected nodes and verify no Lustre mounts
remain before reverting an arbitrary custom entrypoint. **The custom entrypoint
is not sticky** -- each install must explicitly request it.

#### Revert with Helm

Delete the ConfigMap, then replace each node pod only after safely draining its
node and verifying that no Lustre mounts remain:

```shell
kubectl delete configmap csi-azurelustre-entrypoint -n kube-system --ignore-not-found
kubectl delete pod -n kube-system <drained-node-csi-pod>
kubectl wait -n kube-system --for=condition=Ready \
  pod/<replacement-node-csi-pod> --timeout=10m
kubectl uncordon <node-name>
```

### Important Notes

- The custom entrypoint replaces the **entire** built-in entrypoint, including Lustre client installation logic, and is used by **both** the `lustre-loader` sidecar and the `azurelustre` driver container. Your custom script is responsible for all per-role setup (see the role table under [How It Works](#how-it-works)) — including loading the kernel modules and bringing up LNet in the `loader` role — and, in the `driver` role, for launching the CSI driver binary.
- A good starting point for a custom entrypoint is the built-in script at `pkg/azurelustreplugin/entrypoint.sh`, which already dispatches on `AZURELUSTRE_CSI_ROLE`. Copy and adapt it rather than writing a single-flow script, so that both the `loader` and `driver` roles are handled.
- Kubelet runs `/app/readinessProbe.sh` directly; a custom entrypoint does not override it. Custom loaders must preserve startup-state records and LNet health or the Pod stays NotReady. In legacy startup mode, a failing loader startup probe also blocks the driver and registrar from starting.
- **Migration note:** earlier driver versions selected behavior with `AZURELUSTRE_CSI_INSTALL_LUSTRE_CLIENT` (`yes`/`no`); this has been replaced by `AZURELUSTRE_CSI_ROLE` (`loader`/`driver`/`controller`). A custom entrypoint carried over from before the sidecar split must be updated to read `AZURELUSTRE_CSI_ROLE` and implement the `loader` and `driver` roles separately.
- **Security note:** the custom entrypoint is stored in the `csi-azurelustre-entrypoint` ConfigMap in `kube-system` and is executed by a privileged container. Treat this as a code-injection path: tightly restrict RBAC for creating or updating this ConfigMap, and only use custom entrypoints in trusted/admin scenarios.
- Editing the ConfigMap does not restart running entrypoints. New or restarted
  containers read the current configuration. Replace each pod only after safely
  draining its node and verifying that no Lustre mounts remain.
- The uninstall script automatically cleans up the ConfigMap if it exists.
