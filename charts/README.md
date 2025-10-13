# Install Azure Lustre CSI Driver with Helm 3

## Add Helm Repo

To add the Helm repo:

```console
helm repo add azurelustre-csi-driver https://raw.githubusercontent.com/kubernetes-sigs/azurelustre-csi-driver/main/charts
helm repo update
```

## Install latest released chart

Installs released version (e.g. `0.3.0`):

```console
helm install azurelustre azurelustre-csi-driver/azurelustre-csi-driver --namespace kube-system --create-namespace --version 0.3.0
```

## Install snapshot (latest development)

Use the in-repo `latest` chart (unreleased main branch content):

```console
helm install azurelustre ./charts/latest/azurelustre-csi-driver --namespace kube-system --create-namespace
```

Not for production. Image defaults to `latest` tag.

## Install from working copy

```console
helm install azurelustre ./charts/v0.3.0/azurelustre-csi-driver --namespace kube-system
```

## Install a specific version (after repo add)

```console
helm install azurelustre azurelustre-csi-driver/azurelustre-csi-driver --namespace kube-system --version 0.3.0
```

## Search for all available versions

```console
helm search repo -l azurelustre-csi-driver
```

## Upgrade (example: bump driver image tag only)

```console
helm upgrade azurelustre azurelustre-csi-driver/azurelustre-csi-driver --namespace kube-system --set image.tag=v0.3.1
```

Or from local chart:

```console
helm upgrade azurelustre ./charts/v0.3.0/azurelustre-csi-driver --namespace kube-system --set image.tag=v0.3.1
```

## Uninstall

```console
helm uninstall azurelustre -n kube-system
```

## Tips

- Dry run rendering: `helm template test ./charts/v0.3.0/azurelustre-csi-driver -n kube-system | less`
- Skip Lustre client install on nodes: `--set node.lustreClient.install=false`
- Change Lustre client version: `--set node.lustreClient.version=2.15.6 --set node.lustreClient.shaSuffix=<sha>`
- Increase verbosity: `--set controller.extraArgs={"-v=5"} --set node.extraArgs={"-v=5"}`
- Force image pull always: `--set image.pullPolicy=Always`

## latest chart configuration

Key configurable parameters from `values.yaml` (latest snapshot) and defaults:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Driver image repository | `mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi` |
| `image.tag` | Driver image tag | `v0.3.0` (released) / `latest` (snapshot) |
| `image.pullPolicy` | Driver image pull policy | `IfNotPresent` |
| `sidecars.provisioner.repository` | csi-provisioner sidecar image | `mcr.microsoft.com/oss/kubernetes-csi/csi-provisioner` |
| `sidecars.provisioner.tag` | csi-provisioner image tag | `v5.1.0` |
| `sidecars.livenessProbe.repository` | liveness probe image | `mcr.microsoft.com/oss/kubernetes-csi/livenessprobe` |
| `sidecars.livenessProbe.tag` | liveness probe image tag | `v2.14.0` |
| `sidecars.nodeDriverRegistrar.repository` | node-driver-registrar image | `mcr.microsoft.com/oss/kubernetes-csi/csi-node-driver-registrar` |
| `sidecars.nodeDriverRegistrar.tag` | node-driver-registrar image tag | `v2.12.0` |
| `controller.enabled` | Deploy controller Deployment | `true` |
| `controller.replicas` | Controller replicas | `2` |
| `controller.priorityClassName` | Controller pod priority class | `system-cluster-critical` |
| `controller.extraArgs` | Extra args passed to controller driver | `["-v=5","--enable-azurelustre-mock-dyn-prov=false"]` |
| `node.priorityClassName` | Node pod priority class | `system-node-critical` |
| `node.lustreClient.install` | Install Lustre client on nodes | `true` |
| `node.lustreClient.version` | Lustre client version | `2.15.5` |
| `node.lustreClient.shaSuffix` | Lustre client SHA suffix | `41-gc010524` |
| `node.extraArgs` | Extra args passed to node driver | `["-v=5"]` |
| `rbac.create` | Create RBAC resources | `true` |
| `csidriver.create` | Create CSIDriver object | `true` |
| `csidriver.name` | CSIDriver name | `azurelustre.csi.azure.com` |
| `csidriver.fsGroupPolicy` | FSGroupPolicy | `File` |
| `paths.kubelet` | Host kubelet path | `/var/lib/kubelet` |
| `paths.kubernetes` | Host Kubernetes config path | `/etc/kubernetes` |
| `paths.dev` | Host /dev path | `/dev` |
| `paths.osRelease` | Host OS release file | `/etc/os-release` |
| `imagePullSecrets` | Image pull secrets array | `[]` |

For full parameter set see `charts/latest/azurelustre-csi-driver/values.yaml`.

For development details see repository root `README.md` and docs in `docs/`.
