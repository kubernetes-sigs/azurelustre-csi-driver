# Install Azure Lustre CSI Driver with Helm 3

## Add Helm Repo

To add the Helm repo:

```console
helm repo add azurelustre-csi-driver https://raw.githubusercontent.com/kubernetes-sigs/azurelustre-csi-driver/main/charts
helm repo update
```

## Install latest released chart

Installs released version (e.g. `0.5.0`):

```console
helm install azurelustre azurelustre-csi-driver/azurelustre-csi-driver --namespace kube-system --create-namespace --version 0.5.0
```

## Install snapshot (latest development)

Use the in-repo `latest` chart (unreleased development branch content):

```console
helm install azurelustre ./charts/latest/azurelustre-csi-driver --namespace kube-system --create-namespace
```

Not for production. Image defaults to `latest` tag, which is not a publicly released tag.

## Install from working copy

```console
helm install azurelustre ./charts/latest/azurelustre-csi-driver --namespace kube-system
```

## Install a specific version (after repo add)

```console
helm install azurelustre azurelustre-csi-driver/azurelustre-csi-driver --namespace kube-system --version 0.5.0
```

## Search for all available versions

```console
helm search repo -l azurelustre-csi-driver
```

## Upgrade (example: bump driver image tag only)

```console
helm upgrade azurelustre azurelustre-csi-driver/azurelustre-csi-driver --namespace kube-system --set image.tag=v0.5.1
```

Or from local chart:

```console
helm upgrade azurelustre ./charts/latest/azurelustre-csi-driver --namespace kube-system --set image.tag=v0.5.1
```

## Uninstall

```console
helm uninstall azurelustre -n kube-system
```

## Tips

- Dry run rendering: `helm template test ./charts/latest/azurelustre-csi-driver -n kube-system | less`
- Skip Lustre client install on nodes: `--set node.lustreClient.install=false`
- Change Lustre client version (per flavor): `--set node.jammy.lustreClient.version=2.15.7 --set node.jammy.lustreClient.shaSuffix=<sha>` (similarly for `node.noble`)
- Force image pull always: `--set image.pullPolicy=Always`

## latest chart configuration

Key configurable parameters from `values.yaml` (latest snapshot) and defaults:

| Parameter | Description | Default |
| --- | --- | --- |
| `image.repository` | Driver image repository | `mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi` |
| `image.tag` | Driver image tag | `v0.5.0` (released) <br> `latest` (snapshot) |
| `image.pullPolicy` | Driver image pull policy | `Always` |
| `sidecars.provisioner.repository` | csi-provisioner sidecar image | `mcr.microsoft.com/oss/kubernetes-csi/csi-provisioner` |
| `sidecars.provisioner.tag` | csi-provisioner image tag | `v5.2.0` |
| `sidecars.livenessProbe.repository` | liveness probe image | `mcr.microsoft.com/oss/kubernetes-csi/livenessprobe` |
| `sidecars.livenessProbe.tag` | liveness probe image tag | `v2.15.0` |
| `sidecars.nodeDriverRegistrar.repository` | node-driver-registrar image | `mcr.microsoft.com/oss/kubernetes-csi/csi-node-driver-registrar` |
| `sidecars.nodeDriverRegistrar.tag` | node-driver-registrar image tag | `v2.13.0` |
| `controller.replicas` | Controller replicas | `2` |
| `controller.priorityClassName` | Controller pod priority class | `system-cluster-critical` |
| `controller.extraArgs` | Extra args passed to controller driver | `["-v=5"]` |
| `node.priorityClassName` | Node pod priority class | `system-node-critical` |
| `node.updateStrategy.maxUnavailable` | Max node pods unavailable during a rolling update | `10%` |
| `node.lustreClient.install` | Install Lustre client on nodes | `true` |
| `node.jammy.lustreClient.version` | Lustre client version for jammy flavor | `2.15.8` |
| `node.jammy.lustreClient.shaSuffix` | Lustre client SHA suffix for jammy flavor | `34-gc0f2040` |
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
