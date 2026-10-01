# Install Azure Lustre CSI Driver with Helm 3

## Released chart versions

| Chart version | Driver image family |
| --- | --- |
| 0.6.0 | v0.6.0 |

The uninstall guard described below is **unreleased development functionality**;
it is not included in chart `0.6.0` or driver `v0.6.0`. Follow the safe teardown
procedure even when the installed version has no guard.

Install only versions listed above. To see every chart tag currently published to the
registry — including pre-release and customer-hidden preview tags that are **not**
supported for general install — list them directly:

    curl -s https://mcr.microsoft.com/v2/microsoft.azuremanagedlustre/azurelustre-csi-driver/tags/list | jq -r '.tags[]'

## Install a released chart

Released charts are OCI artifacts in MCR. Set `CHART_VERSION` to the exact Helm
chart version (for example: `0.6.0`) listed under [Released chart versions](#released-chart-versions).
The chart version is also its immutable OCI tag. The table maps it to the
independent driver image family selected by `image.tag`; `appVersion` reports
that driver release as informational metadata.

    CHART_VERSION=0.6.0
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

An in-place upgrade preserves existing PVCs and PVs and does not run the
pre-delete hook. Do not remove volume objects merely to upgrade the driver.
For manifest installations, follow the [upgrade-in-place
instructions](../docs/install-csi-driver.md#install-with-kubectl) instead of
uninstall/reinstall.

> [!IMPORTANT]
> **Stop every workload using Lustre on the affected nodes before upgrading.** An
> upgrade restarts the node pods. If the release changes the Lustre client
> version, the new kernel modules can only load once the old ones are unloaded,
> and the kernel refuses to unload them while any Lustre filesystem is mounted.
> Drain or scale down every pod holding a Lustre volume first, and **wait until
> the volumes are actually unmounted before upgrading** -- deleting a pod returns
> before the kubelet has finished unmounting its volumes, and an upgrade started
> in that window hits a mount that is still going away.

    CHART_VERSION=0.6.0
    helm upgrade azurelustre --wait \
      oci://mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver \
      --namespace kube-system \
      --version "${CHART_VERSION}"

Or from local chart:

    helm upgrade azurelustre ./charts/latest/azurelustre-csi-driver --namespace kube-system

If Lustre volumes are still mounted when the new node pods start and the release
changes the client version, the upgrade does not take effect on those nodes: the
old client stays resident, mounts keep using the old version, and the
`lustre-loader` container logs a `WARNING` naming both versions. Stop the
workloads and restart the node pods to complete the upgrade.

## Uninstall

### Scope and prerequisites

The development chart enables `preDeleteGuard.enabled` by default. Its Helm
pre-delete Job blocks uninstall if **any** PersistentVolume (PV) has
`spec.csi.driver` equal to the configured `csidriver.name`. This is a
cluster-wide check, not a check of only the release namespace or mounted volumes.
Static, dynamic, `Retain`, unbound, released, and terminating PVs all count.
The guard also blocks uninstall if it cannot complete the check (fail-closed).
It does not delete PVs, PVCs, data, or Azure resources for you.

Use a **matching chart and driver build** that implements
`/app/azurelustreplugin --pre-delete-check`; the released `v0.6.0` binary does
not. Pairing the development chart with that older binary blocks uninstall even
with no PVs. The hook uses the selected driver image's `-noble` flavor on every
node OS, inherits `image.pullPolicy` (default `Always`), and needs registry access
at uninstall time. An explicit `preDeleteGuard.imagePullPolicy=IfNotPresent`
override is appropriate only with an immutable image tag; it is not a fix for
an incompatible or stale image.

The examples below use Bash, `kubectl`, and Helm 3.7 or later. Confirm your Kubernetes
context, then set these to the installed release's values:

    kubectl config current-context
    RELEASE=azurelustre
    NAMESPACE=kube-system
    DRIVER=azurelustre.csi.azure.com
    kubectl get csidriver "${DRIVER}"

Verify that `DRIVER` identifies the intended installation. A query using the
wrong name can return an empty PV list even while the real driver has volumes.

### Safe teardown procedure

This procedure removes the driver and its volume objects. For a repair or
in-place upgrade that must preserve PVCs/PVs, use [Upgrade](#upgrade) instead.

1. **Stop provisioning and consumers, not the driver.** Pause automation that can
   recreate workloads or PVCs, stop pods using Lustre, and wait for their mounts
   to be released. Keep the controller and node plugins running to finish
   `DeleteVolume` and unmount operations. An empty PV list is not enough if a
   `CreateVolume` is still creating an AMLFS filesystem but has not produced its
   PV. Wait for in-flight provisioning to finish and keep provisioning stopped
   throughout teardown.

2. **Inventory every matching PV and decide what data to keep.** This read-only
   query prints PV name, claim namespace/name, reclaim policy, and phase. Change
   `DRIVER` above if the installation overrides `csidriver.name`.

       kubectl get pv -o "jsonpath={range .items[?(@.spec.csi.driver==\"${DRIVER}\")]}{.metadata.name}{\"\t\"}{.spec.claimRef.namespace}{\"/\"}{.spec.claimRef.name}{\"\t\"}{.spec.persistentVolumeReclaimPolicy}{\"\t\"}{.status.phase}{\"\n\"}{end}"

   A query error is **not** proof that no PVs exist. Resolve API access errors
   before proceeding. Record the PV specifications and backing filesystem
   identifiers needed for recovery or reattachment.

   | Volume / intended outcome | Required handling before uninstall |
   | --- | --- |
   | Dynamically created AMLFS, delete data | With `Delete` on the PV, deleting its PVC requests deletion of the **backing AMLFS filesystem and its data**. Confirm that deletion is intended and backups are adequate first. |
   | Dynamically created AMLFS, preserve data | Set the **existing PV's** reclaim policy to `Retain` and verify it **before** deleting the claim. Changing only the StorageClass does not update existing PVs. |
   | Static PV using an existing filesystem | Keep the customer-owned filesystem. Use `Retain` when removing its Kubernetes objects and preserve the mapping needed to reattach it. Do not delete the Azure filesystem merely to clear the guard. |

   For a specific PV whose data must be retained, first save its definition:

       PV=replace-with-pv-name
       kubectl get pv "${PV}" -o yaml > "${PV}.yaml"

   Stop if the export fails; verify the saved file contains the intended PV and
   filesystem settings. It is a **reference**, not a manifest to reapply as-is:
   it includes server-managed metadata, status, and a claim reference with the
   old PVC's identity. Then change and verify the reclaim policy before deletion:

       kubectl patch pv "${PV}" --type=merge \
         -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'
       kubectl get pv "${PV}" -o custom-columns=NAME:.metadata.name,RECLAIM:.spec.persistentVolumeReclaimPolicy

   `Retain` is not a backup and does not undo a deletion already in progress.
   See [dynamic volume deletion](../docs/dynamic-provisioning.md#delete-the-volume)
   and [Kubernetes reclaim policies](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#reclaiming).

3. **Remove only the claims and PV objects covered by that plan.** Delete each
   selected PVC in its own namespace after its consumers stop. For `Delete`,
   let the still-running driver finish backend deletion and wait for the PV to
   disappear; AMLFS deletion can take ten minutes or more. For `Retain`, the
   PV normally remains `Released`: after confirming no consumers or claims
   remain and preserving its mapping, delete that selected PV object. The
   retained filesystem remains, continues to incur charges, and is your
   responsibility to reattach or eventually delete. To reattach after installing
   the driver, use fresh PV/PVC manifests from the [static provisioning
   guide](../docs/static-provisioning.md#option-2-use-pv), the saved filesystem
   settings, and a `Retain` policy; do not replay the old object's metadata or
   claim binding.

   Do not bulk-delete PVs or strip PV/PVC finalizers to get past the guard.
   A stuck `Terminating` object needs its consumers, events, controller logs,
   and backend operation investigated while the driver is still installed.
   Re-run the inventory query; it must succeed with no matching PVs.

4. **Uninstall through the installation's owner.** For a direct Helm installation:

       helm uninstall "${RELEASE}" -n "${NAMESPACE}" --wait --timeout 6m

   Six minutes allows the default five-minute hook Job deadline to finish.
   Adjust the Helm timeout if you intentionally change the Job deadline.
   For a manifest installation, run `./deploy/uninstall-driver.sh` from the
   matching checkout instead. The current script targets the default
   `azurelustre.csi.azure.com` driver and the manifest installation in `kube-system`;
   the Helm variables above do not configure that script.
   For an Azure-managed extension, use the
   [extension deletion path](#azure-managed-extensions), not direct Helm removal.

5. **Verify removal rather than relying on cluster deletion.** For direct Helm:

       helm list -n "${NAMESPACE}" --all --filter "^${RELEASE}$"
       kubectl get deployments,daemonsets,pods -n "${NAMESPACE}" \
         -l "app.kubernetes.io/instance=${RELEASE}"
       kubectl get csidriver "${DRIVER}" --ignore-not-found

   The release and driver resources should be absent. API/authentication errors
   are not absence. Separately confirm the intended backend outcome: deleted
   AMLFS resources for `Delete`, or preserved filesystems for `Retain`.

### Troubleshoot a blocked uninstall

Helm may report a failed pre-delete hook and leave the release in `uninstalling`.
That does not by itself mean the driver was removed. Inspect it before retrying:

    GUARD_SELECTOR="app.kubernetes.io/instance=${RELEASE},app.kubernetes.io/component=predelete-guard"
    helm status "${RELEASE}" -n "${NAMESPACE}"
    kubectl get deployments,daemonsets,pods -n "${NAMESPACE}" \
      -l "app.kubernetes.io/instance=${RELEASE}"
    kubectl get jobs,pods -n "${NAMESPACE}" -l "${GUARD_SELECTOR}" -o wide
    kubectl describe jobs -n "${NAMESPACE}" -l "${GUARD_SELECTOR}"
    kubectl describe pods -n "${NAMESPACE}" -l "${GUARD_SELECTOR}"
    kubectl logs -n "${NAMESPACE}" -l "${GUARD_SELECTOR}" \
      -c predelete-guard --tail=-1

**Save the output before retrying.** The failed Job is eligible for deletion
after `preDeleteGuard.ttlSecondsAfterFinished` (default 300 seconds) following
completion. This is not a guaranteed pod-log retention window: for example, the
Job controller can delete active pods when the Job deadline expires. Capture Job
events even if no pod or logs remain. A new uninstall attempt also replaces the
previous hook resources; successful hook Jobs are deleted. A missing old Job
is not evidence that the guard never ran.

| Observation | Action |
| --- | --- |
| `found ... PersistentVolume(s) still using CSI driver ...` | Expected safety rejection. Inspect the named PVs and follow the data-retention procedure above. Unmounting alone does not clear the check. |
| `Forbidden` or `list PersistentVolumes` errors | Restore Kubernetes API connectivity/authorization. The hook ServiceAccount needs cluster-wide `list` on `persistentvolumes`; if `rbac.create=false`, supply that RBAC yourself. Azure `Contributor` does not grant this Kubernetes permission. |
| `ImagePullBackOff`, no pod logs, or a pending pod | Inspect pod events, registry credentials/egress, available nodes, taints and resource capacity. These are operational failures, not proof of matching PVs. |
| Job exists but no pods were created | Inspect `kubectl describe jobs` events for admission, ServiceAccount, quota, or priority-class restrictions in the release namespace. There may be no pod logs to retrieve. |
| Unknown `--pre-delete-check` flag | The selected image is incompatible. A deployed release needs a matching chart/image pair. If it is already `uninstalling`, see the recovery limitation below. An empty PV list alone does not establish safe teardown. |
| API timeout or Job deadline exceeded | Inspect connectivity and scheduling first. `checkTimeout` (default `30s`) bounds the check; `activeDeadlineSeconds` (default `300`) bounds the Job. These are separate from Helm's timeout. |

After fixing the cause and confirming the PV inventory is empty, retry the
**ordinary** `helm uninstall` command from step 4. Do not delete Helm release
secrets or driver resources manually to clear an `uninstalling` status.

**Incompatible-image recovery:** Helm may refuse an upgrade once the release is
`uninstalling`, so changing chart values at that point is not a reliable repair.
Restore registry access if that is the problem. If the stored hook cannot run
a compatible image, involve the installation owner. For a direct Helm release,
an explicitly approved `--no-hooks` completion is possible only after manually
completing the teardown checks: no matching PVs, no consumers/mounts or in-flight
provisioning, and the intended backend cleanup finished. Review
`helm get hooks "${RELEASE}" -n "${NAMESPACE}"` first because this skips every
hook, not just the PV check. See [emergency bypasses](#emergency-bypasses).

A timeout from `--wait` can also occur **after** Helm has started deleting
resources. Use step 5 to determine what remains; do not assume every timeout
means the pre-delete guard rejected removal.

### Emergency bypasses

Bypasses are an explicit acceptance of incomplete cleanup, not routine recovery.
They can strand mounts, leave PVs/PVCs terminating, and orphan billable AMLFS
filesystems. The operator must arrange any remaining unmount/backend cleanup.

- **Direct Helm:** `helm uninstall "${RELEASE}" -n "${NAMESPACE}" --no-hooks`
  skips **all** hooks. Alternatively, configure `preDeleteGuard.enabled=false`
  in the installed release before uninstalling. Editing a local `values.yaml`
  alone does not change the stored release. An upgrade to change release values
  can restart driver pods; follow the [upgrade precautions](#upgrade).
- **Manifest installation:** `./deploy/uninstall-driver.sh --force` bypasses
  that script's PV query only. Older script versions may not implement the check.
- **Azure extension:** `az k8s-extension delete --force` is **not** the equivalent
  of either bypass above; see the warning below.

### Azure-managed extensions

> [!CAUTION]
> The guard is not an Azure extension deletion guard. Azure deletes an AKS
> cluster extension resource immediately and connected agents remove its Helm
> release asynchronously. A failed hook can preserve the in-cluster release,
> but it cannot preserve the Azure extension resource. Confirm that no matching
> PersistentVolumes or provisioning operations remain before deleting an
> extension instance. See [Delete extension
> instance](https://learn.microsoft.com/azure/aks/deploy-extensions-az-cli#delete-extension-instance).

Do not use `az k8s-extension delete --force` as a hook bypass: it can leave
the Helm release and driver workloads behind as an unmanaged installation.
Successful direct-Helm validation does not qualify deletion through the Azure
extension agent; validate that path with the matching published extension build.

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
