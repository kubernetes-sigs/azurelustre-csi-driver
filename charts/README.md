# Install Azure Lustre CSI Driver with Helm 3

## Released chart versions

| Chart version | Driver image family |
| --- | --- |

Install only versions listed above. To see every chart tag currently published to the
registry — including pre-release and customer-hidden preview tags that are **not**
supported for general install — list them directly:

    curl -s https://mcr.microsoft.com/v2/microsoft.azuremanagedlustre/azurelustre-csi-driver/tags/list | jq -r '.tags[]'

## Install a released chart

Released charts are OCI artifacts in MCR. Set `CHART_VERSION` to the exact Helm
chart version (`A.B.C`) listed under [Released chart versions](#released-chart-versions).
The chart version is also its immutable OCI tag. The table maps it to the
independent driver image family selected by `image.tag`; `appVersion` reports
that driver release as informational metadata.

    CHART_VERSION=A.B.C
    helm install azurelustre \
      oci://mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver \
      --namespace kube-system --create-namespace \
      --version "${CHART_VERSION}"

## Install snapshot (latest development)

Use the in-repo `latest` chart (unreleased development branch content):

    helm install azurelustre ./charts/latest/azurelustre-csi-driver --namespace kube-system --create-namespace

The chart uses the unreleased `latest` image by default.

## Install from working copy

    helm install azurelustre ./charts/latest/azurelustre-csi-driver --namespace kube-system

## Upgrade

> [!IMPORTANT]
> Node DaemonSets default to `OnDelete`. A Helm upgrade stages the new pod
> template but does not replace existing node pods solely because the template
> changed. Existing nodes continue using their current CSI node pod and loaded
> Lustre client until the pod or node is replaced through a safe lifecycle.

    CHART_VERSION=A.B.C
    helm upgrade azurelustre \
      oci://mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver \
      --namespace kube-system \
      --version "${CHART_VERSION}"

Or from local chart:

    helm upgrade azurelustre ./charts/latest/azurelustre-csi-driver --namespace kube-system

Newly created eligible node pods use the current template. Before manually
deleting an existing node pod, cordon and drain the node and verify that no
Lustre mounts remain. Do not use `kubectl rollout restart` to activate a new
client on mounted nodes.

Standalone Helm users can opt into Kubernetes-managed rolling replacement with
`--set node.updateStrategy.type=RollingUpdate`. Before doing so, drain every
affected node and verify that its Lustre filesystems are fully unmounted.

## Uninstall

    helm uninstall azurelustre -n kube-system

## Tips

- Dry run rendering: `helm template test ./charts/latest/azurelustre-csi-driver -n kube-system | less`
- Force image pull always: `--set image.pullPolicy=Always`

## Chart configuration

> [!WARNING]
> `image.tag` and `node.<flavor>.lustreClient.*` are exposed for debugging and
> hotfixes only. Each chart release is validated with a specific driver image
> family and Lustre client version together, and overriding either on its own
> breaks that pairing. Do not set them unless Microsoft support directs you to.

The chart manages the `csi-provisioner` arguments as part of the supported
driver configuration. In particular, it preserves the complete PVC UID in
dynamically provisioned volume names by setting
`--volume-name-uuid-length=-1`.

Key configurable parameters and defaults from the version-neutral source
`values.yaml`. The release pipeline replaces `image.tag` with the selected
driver image family when it packages a chart:

| Parameter | Description | Default |
| --- | --- | --- |
| `image.repository` | Driver image repository | `mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi` |
| `image.tag` | Driver image family base; OS-specific templates append a flavor suffix | `latest` |
| `image.pullPolicy` | Driver image pull policy | `Always` |
| `sidecars.provisioner.repository` | csi-provisioner sidecar image | `mcr.microsoft.com/oss/kubernetes-csi/csi-provisioner` |
| `sidecars.provisioner.tag` | csi-provisioner image tag | `v5.2.0` |
| `sidecars.livenessProbe.repository` | liveness probe image | `mcr.microsoft.com/oss/kubernetes-csi/livenessprobe` |
| `sidecars.livenessProbe.tag` | liveness probe image tag | `v2.15.0` |
| `sidecars.nodeDriverRegistrar.repository` | node-driver-registrar image | `mcr.microsoft.com/oss/kubernetes-csi/csi-node-driver-registrar` |
| `sidecars.nodeDriverRegistrar.tag` | node-driver-registrar image tag | `v2.13.0` |
| `controller.replicas` | Controller replicas | `2` |
| `controller.priorityClassName` | Controller pod priority class | `system-cluster-critical` |
| `controller.tolerations` | Controller pod tolerations (control-plane taints only; does not tolerate `CriticalAddonsOnly`) | control-plane `master`/`controlplane`/`control-plane` |
| `controller.extraArgs` | Extra args passed to controller driver | `["-v=5"]` |
| `node.priorityClassName` | Node pod priority class | `system-node-critical` |
| `node.tolerations` | Node DaemonSet tolerations. Tolerates common AKS user-pool taints (spot, GPU) but **not** `CriticalAddonsOnly`, so the plugin stays off reserved/tainted system pools. Overriding **replaces** this set — for custom-tainted Lustre pools, include the spot/GPU entries you still need. | spot + `sku=gpu` + `nvidia.com/gpu` |
| `node.updateStrategy.type` | Node DaemonSet update strategy. `OnDelete` stages changes without replacing existing node pods. | `OnDelete` |
| `node.updateStrategy.rollingUpdate.maxUnavailable` | Maximum unavailable node pods when explicitly using `RollingUpdate` | `10%` |
| `node.jammy.lustreClient.version` | Lustre client version for jammy flavor | `2.15.8` |
| `node.jammy.lustreClient.shaSuffix` | Lustre client SHA suffix for jammy flavor | `39-g2d32b59` |
| `node.noble.lustreClient.version` | Lustre client version for noble flavor | `2.17.0` |
| `node.noble.lustreClient.shaSuffix` | Lustre client SHA suffix for noble flavor | `24-gf517bc4` |
| `node.azurelinux3.lustreClient.version` | Lustre client version for azurelinux3 flavor | `2.17.0` |
| `node.azurelinux3.lustreClient.shaSuffix` | Lustre client SHA suffix for azurelinux3 flavor | `24-gf517bc4` |
| `node.extraArgs` | Extra args passed to node driver | `["-v=5"]` |
| `rbac.create` | Create RBAC resources | `true` |
| `csidriver.name` | CSIDriver name | `azurelustre.csi.azure.com` |
| `csidriver.fsGroupPolicy` | FSGroupPolicy | `File` |
| `IsWorkloadIdentityEnabled` | Enable controller workload identity | `Disabled` |
| `IdentityClientId` | Workload identity client ID (required when enabled) | `""` |
| `IdentityTenantId` | Optional cross-tenant workload identity tenant ID | `""` |
| `paths.kubelet` | Host kubelet path | `/var/lib/kubelet` |
| `paths.kubernetes` | Host Kubernetes config path | `/etc/kubernetes` |
| `paths.dev` | Host /dev path | `/dev` |
| `paths.osRelease` | Host OS release file | `/etc/os-release` |
| `imagePullSecrets` | Image pull secrets array | `[]` |

For full parameter set see `charts/latest/azurelustre-csi-driver/values.yaml`.

For development details see repository root `README.md` and docs in `docs/`.
