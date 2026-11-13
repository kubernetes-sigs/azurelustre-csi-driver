# Install Azure Lustre CSI Driver with Helm 3

## Released chart versions

| Chart version | Driver image family |
| --- | --- |
| 0.7.0 | v0.7.0 |
| 0.6.0 | v0.6.0 |

Install only versions listed above. To see every chart tag currently published to the
registry — including pre-release and customer-hidden preview tags that are **not**
supported for general install — list them directly:

    curl -s https://mcr.microsoft.com/v2/microsoft.azuremanagedlustre/azurelustre-csi-driver/tags/list | jq -r '.tags[]'

## Install a released chart

These commands are for a new installation. If the driver is already installed,
see [Upgrade](#upgrade) before changing versions.

Released charts are OCI artifacts in MCR. Set `CHART_VERSION` to the exact Helm
chart version (for example: `0.7.0`) listed under [Released chart versions](#released-chart-versions).
The chart version is also its immutable OCI tag. The table maps it to the
independent driver image family selected by `image.tag`; `appVersion` reports
that driver release as informational metadata.

    CHART_VERSION=0.7.0
    helm install azurelustre --wait \
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

Use the procedure that matches your installation method. A direct Helm release
can be updated with `helm upgrade`; a legacy script installation uses
[driver replacement](../docs/install-csi-driver.md#install-with-kubectl).
For a managed extension, use the
[extension update guidance](https://learn.microsoft.com/azure/aks/azure-managed-lustre-upgrade-uninstall-cli-preview#upgrade-an-aks-cli-installation-preview),
not Helm or the cleanup script.

> [!IMPORTANT]
> **Stop every workload using Lustre on the affected nodes before updating the
> driver.** Updating the driver restarts node pods. If the Lustre client version
> changes, the new kernel modules can load only after the old modules unload;
> active Lustre mounts prevent that unloading.
> **Wait until the volumes are actually unmounted**: deleting a workload pod can
> return before the kubelet finishes unmounting its volumes.
>
> Updating or replacing the driver is not storage cleanup. Keep the existing PVs,
> PVCs, backing filesystems, and AKS cluster.
>
> For **static provisioning**, deleting a Kubernetes volume does not delete the
> existing AMLFS filesystem or its data, but removes the Kubernetes configuration
> needed to use it. For **dynamic provisioning**, deleting a PVC whose PV has
> reclaim policy `Delete` can delete the provisioned AMLFS filesystem and its data.
> Check StatefulSet claim retention before scaling down workloads; scale-down
> can delete claims when `whenScaled` is `Delete`.

### Update a direct Helm release

The steps below target an existing 0.6.0 Helm release and chart 0.7.0. Confirm
the transition and your configuration with Microsoft Support before proceeding.
The published charts retain the same resource names, selectors, CSI identity,
and Lustre client defaults for the same release name, namespace, and default
values. That comparison alone does not validate customized installations or
other version transitions.

1. Complete the [preparation, claim-retention, and unmount
   checks](https://learn.microsoft.com/azure/aks/azure-managed-lustre-uninstall#prepare-for-the-upgrade).
   Stop Lustre workloads and wait for their volumes to unmount while the old
   driver is still running. Pause new provisioning and wait for in-flight
   volume operations to finish.

2. Export the existing release's custom values. Use its release name and
   namespace throughout:

       helm get values azurelustre --namespace kube-system --output yaml > azurelustre-values.yaml

   Review the values for chart 0.7.0, including image/client overrides, Workload
   Identity, and custom entrypoints. Keep the existing CSI driver identity and
   naming overrides. Don't blindly reuse old values or change `csidriver.name`.

3. Update the existing release without uninstalling it first:

       helm upgrade azurelustre --wait --timeout 30m \
         oci://mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver \
         --namespace kube-system \
         --version 0.7.0 \
         --reset-values \
         --values azurelustre-values.yaml

   Helm updates release resources, and Kubernetes rolls the pods. Don't use
   `--force` to bypass an incompatible resource or take over another
   installation's objects.

4. Verify controller and node readiness and the loaded Lustre client version on
   every affected node. Then restart workloads with their original claims and
   confirm normal I/O. See [Verify the
   upgrade](https://learn.microsoft.com/azure/aks/azure-managed-lustre-uninstall#verify-the-upgrade).
   A successful `helm --wait` alone does not verify the loaded client or I/O.

If old modules remain loaded when the client version changes, mounts can keep
using the old client. The `lustre-loader` logs a `WARNING` naming the loaded and
requested versions. Protect claims, stop consumers, and verify actual unmounts
before restarting the affected node pods so their loaders can retry module
cleanup. Verify the loaded version before resuming workloads; see
[module cleanup and recovery](../docs/csi-debug.md#teardown-behavior-sigterm-and-termination).

For an older or customized Helm release, a change of installation method, or
replacement of a guarded v0.7.0 installation, obtain a matching procedure from
Microsoft Support. Don't use the permanent-removal steps below as an upgrade
shortcut or delete volumes to clear the uninstall guard.

### Update from a working copy (development)

For development or testing of an existing direct Helm release, Helm can use a
local chart instead of the OCI chart. Complete the same preparation, values
export/review, and verification checks above, using the existing release name
and namespace:

    helm upgrade azurelustre ./charts/latest/azurelustre-csi-driver \
      --namespace kube-system --wait --timeout 30m \
      --reset-values --values azurelustre-values.yaml

The working-copy chart is unreleased and defaults to the `latest` image. Review
its templates, image/client values, and compatibility before use; it is not the
published 0.7.0 chart merely because this checkout describes that release.
This example does not establish a supported production transition or apply to
script-managed or extension-managed installations.

## Uninstall

### Scope and prerequisites

The pre-delete guard is enabled by default. It blocks uninstall while any PV's
`spec.csi.driver` matches `csidriver.name`, cluster-wide and in any PV phase,
including static and retained volumes. It cannot detect a `CreateVolume`
operation before its PV exists. It fails closed if the check cannot run.

These Bash examples require `kubectl` and Helm 3.7+. Confirm the context and use
the installed release's namespace and driver name:

    kubectl config current-context
    RELEASE=azurelustre
    NAMESPACE=kube-system
    DRIVER=azurelustre.csi.azure.com
    kubectl get csidriver "${DRIVER}"

### Safe teardown procedure

For version changes that must preserve volumes, start with [Upgrade](#upgrade).
The cleanup below is for permanent removal.

The stock chart contains driver resources, not application PVs or PVCs. Helm
uninstall removes those driver resources, not the backing AMLFS filesystems.
The volume-cleanup actions below are separate and can delete data according to
the provisioning method and reclaim policy.

1. **Stop provisioning and workloads.** Pause automation that recreates PVCs or
   pods. Wait for unmounts and in-flight provisioning to finish. Keep the driver
   running until volume cleanup completes.

2. **Check PVs and decide what data to keep.** This query lists matching PVs,
   their claims, reclaim policies, and phases:

       kubectl get pv -o "jsonpath={range .items[?(@.spec.csi.driver==\"${DRIVER}\")]}{.metadata.name}{\"\t\"}{.spec.claimRef.namespace}{\"/\"}{.spec.claimRef.name}{\"\t\"}{.spec.persistentVolumeReclaimPolicy}{\"\t\"}{.status.phase}{\"\n\"}{end}"

   A failed query or an incorrect `DRIVER` value is not proof that no PVs exist.

   | Intended outcome | Before deleting the PVC |
   | --- | --- |
   | Delete dynamically provisioned storage | Confirm deletion is intended: the PV's `Delete` reclaim policy deletes the backing AMLFS filesystem and its data. |
   | Preserve dynamically provisioned storage | Set and verify `Retain` on the **existing PV** first. |
   | Preserve a static filesystem | Use `Retain` and save the filesystem mapping for reattachment. Do not delete the Azure filesystem to clear the guard. |

   To change an existing PV's policy, follow [Kubernetes' reclaim-policy
   instructions](https://kubernetes.io/docs/tasks/administer-cluster/change-pv-reclaim-policy/).
   Changing the StorageClass alone does not update existing PVs.

3. **Complete volume cleanup.** Delete only the selected PVCs. With `Delete`,
   wait for backend deletion and the PV to disappear; see [dynamic volume
   deletion](../docs/dynamic-provisioning.md#delete-the-volume). With `Retain`,
   save the filesystem mapping and delete the remaining PV object after its
   claims and consumers are gone. The filesystem remains and continues to incur
   charges; use [static provisioning](../docs/static-provisioning.md#option-2-use-pv)
   to reattach it later.

   Investigate stuck PV/PVC finalizers with the driver still installed; do not
   strip them to force cleanup. Re-run the inventory query until it succeeds
   with no matching PVs.

4. **Uninstall.** For a direct Helm installation:

       helm uninstall "${RELEASE}" -n "${NAMESPACE}" --wait --timeout 6m

   Keep the timeout longer than the hook Job deadline (default five minutes).
   For a manifest installation, run `./deploy/uninstall-driver.sh` from the
   matching checkout; it targets the default driver in `kube-system`, not the
   variables above. For an [Azure-managed extension](#azure-managed-extensions),
   use the extension deletion path instead.

5. **Verify removal.** For direct Helm:

       helm list -n "${NAMESPACE}" --all --filter "^${RELEASE}$"
       kubectl get deployments,daemonsets,pods -n "${NAMESPACE}" \
         -l "app.kubernetes.io/instance=${RELEASE}"
       kubectl get csidriver "${DRIVER}" --ignore-not-found

   The release and driver resources should be absent; API errors are not
   absence. Also confirm the intended filesystem deletion or retention.

### Troubleshoot a blocked uninstall

After a failed hook, Helm may leave the release in `uninstalling` without removing
the driver. Inspect the hook before retrying:

    GUARD_SELECTOR="app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=predelete-guard"
    helm status "${RELEASE}" -n "${NAMESPACE}"
    kubectl get jobs,pods -n "${NAMESPACE}" -l "${GUARD_SELECTOR}" -o wide
    kubectl describe jobs -n "${NAMESPACE}" -l "${GUARD_SELECTOR}"
    kubectl describe pods -n "${NAMESPACE}" -l "${GUARD_SELECTOR}"
    kubectl logs -n "${NAMESPACE}" -l "${GUARD_SELECTOR}" \
      -c predelete-guard --tail=-1

**Save diagnostics before retrying.** Failed Jobs expire after
`preDeleteGuard.ttlSecondsAfterFinished` (default 300 seconds) from completion;
retrying replaces them. Pods/logs can disappear sooner on deadline expiry, so capture
Job events too. Successful hook Jobs are deleted immediately.

| Observation | Action |
| --- | --- |
| `found ... PersistentVolume(s) still using CSI driver ...` | Follow the teardown steps for the named PVs. Unmounting alone is insufficient. |
| `Forbidden` or PV-list errors | Restore API access. The hook ServiceAccount needs cluster-wide `list` on `persistentvolumes`; supply it yourself when `rbac.create=false`. Azure `Contributor` is not Kubernetes RBAC. |
| `ImagePullBackOff` | Check registry access and credentials for the selected `-noble` image. The hook inherits `image.pullPolicy` (default `Always`). |
| Pending or missing pods | Check pod/Job events for scheduling, admission, ServiceAccount, quota, or priority-class failures. |
| Unknown `--pre-delete-check` flag | Use a matching chart and driver image. |
| API timeout or Job deadline exceeded | Check connectivity and scheduling. `checkTimeout` (default `30s`) and `activeDeadlineSeconds` (default `300`) are separate from Helm's timeout. |

Fix the cause, confirm the PV inventory is empty, then retry **ordinary**
`helm uninstall`. Do not delete Helm release secrets to clear the status.

If an incompatible image leaves the release `uninstalling`, Helm may refuse an
upgrade to change it. Involve the installation owner before using an
[emergency bypass](#emergency-bypasses).

A `--wait` timeout can occur after deletion starts. Use step 5 to check what remains.

### Emergency bypasses

Bypassing can strand mounts, PVs, and billable filesystems. Obtain the installation
owner's approval and complete all teardown checks manually first.

- **Direct Helm:** `helm uninstall "${RELEASE}" -n "${NAMESPACE}" --no-hooks`
  skips **all** hooks; inspect `helm get hooks "${RELEASE}" -n "${NAMESPACE}"` first.
- **Manifest installation:** `./deploy/uninstall-driver.sh --force` bypasses
  the script's PV query.

### Azure-managed extensions

> [!CAUTION]
> The guard cannot preserve the Azure extension resource: Azure deletes it before
> agents remove the Helm release asynchronously. Complete the teardown checks
> before [deleting the extension](https://learn.microsoft.com/azure/aks/deploy-extensions-az-cli#delete-extension-instance).

`az k8s-extension delete --force` is **not** a Helm hook bypass; it can leave
the release and driver workloads unmanaged.

**Deleting AKS:** Complete volume cleanup while the driver is still running;
the guard cannot block cluster deletion. Verify backing AMLFS resources
separately, since filesystems outside the deleted resource groups can remain
billable. `Retain` does not protect a filesystem from Azure resource-group deletion.

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

Sidecars use unnumbered release tags from the DALEC-backed
`mcr.microsoft.com/oss/v2/` namespace. These tags track the latest packaged
revision of the same upstream release, including base-image rebuilds, without
pinning the chart to a numbered `-N` rebuild tag. With
`sidecars.pullPolicy: Always`, each container start resolves the current rebuild.
Running containers are not updated until they restart.

Controller replicas coordinate through a leader-election Lease in the release
namespace. The controller service account can manage Leases only in that
namespace; its cluster-wide volume provisioning permissions are unchanged.
The Lease namespace does not limit which namespaces the controller serves.

Install only one Azure Lustre CSI driver per cluster. Installing another copy in
a different namespace does not isolate it: each copy can elect its own leader
while sharing the same cluster-wide driver identity and node socket paths.

Key configurable parameters and defaults from the version-neutral source
`values.yaml`. The release pipeline replaces `image.tag` with the selected
driver image family when it packages a chart:

| Parameter | Description | Default |
| --- | --- | --- |
| `image.repository` | Driver image repository | `mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi` |
| `image.tag` | Driver image family base; OS-specific templates append a flavor suffix | `latest` |
| `image.pullPolicy` | Driver image pull policy | `Always` |
| `sidecars.pullPolicy` | Pull policy for all sidecar images | `Always` |
| `sidecars.provisioner.repository` | csi-provisioner sidecar image | `mcr.microsoft.com/oss/v2/kubernetes-csi/csi-provisioner` |
| `sidecars.provisioner.tag` | csi-provisioner image tag | `v5.3.0` |
| `sidecars.provisioner.healthPort` | csi-provisioner leader-election health port | `29761` |
| `sidecars.livenessProbe.repository` | liveness probe image | `mcr.microsoft.com/oss/v2/kubernetes-csi/livenessprobe` |
| `sidecars.livenessProbe.tag` | liveness probe image tag | `v2.20.0` |
| `sidecars.nodeDriverRegistrar.repository` | node-driver-registrar image | `mcr.microsoft.com/oss/v2/kubernetes-csi/csi-node-driver-registrar` |
| `sidecars.nodeDriverRegistrar.tag` | node-driver-registrar image tag | `v2.18.0` |
| `sidecars.nodeDriverRegistrar.healthPort` | node-driver-registrar HTTP health port | `29764` |
| `controller.replicas` | Controller replicas | `2` |
| `controller.priorityClassName` | Controller pod priority class | `system-cluster-critical` |
| `controller.tolerations` | Controller pod tolerations (control-plane taints only; does not tolerate `CriticalAddonsOnly`) | control-plane `master`/`controlplane`/`control-plane` |
| `controller.extraArgs` | Extra args passed to controller driver | `["-v=5"]` |
| `node.priorityClassName` | Node pod priority class | `system-node-critical` |
| `node.tolerations` | Node DaemonSet tolerations. Tolerates common AKS user-pool taints (spot, GPU) but **not** `CriticalAddonsOnly`, so the plugin stays off reserved/tainted system pools. Overriding **replaces** this set — for custom-tainted Lustre pools, include the spot/GPU entries you still need. | spot + `sku=gpu` + `nvidia.com/gpu` |
| `node.updateStrategy.maxUnavailable` | Max node pods unavailable during a rolling update | `10%` |
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
| `preDeleteGuard.enabled` | Block Helm uninstall while matching PersistentVolumes exist or the check cannot complete | `true` |
| `preDeleteGuard.imagePullPolicy` | Override the hook Job image pull policy; empty inherits `image.pullPolicy` | `""` (inherits `Always`) |
| `preDeleteGuard.checkTimeout` | Kubernetes API timeout for the guard process | `30s` |
| `preDeleteGuard.priorityClassName` | Priority class for the hook Job | `system-cluster-critical` |
| `preDeleteGuard.activeDeadlineSeconds` | Overall hook Job deadline | `300` |
| `preDeleteGuard.ttlSecondsAfterFinished` | TTL for a finished (completed or failed) hook Job; successful hooks are also deleted by Helm | `300` |
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
