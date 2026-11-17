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

An in-place upgrade preserves existing PVCs and PVs and does not run the
pre-delete hook. Do not remove volume objects merely to upgrade the driver.
For manifest installations, follow the [upgrade-in-place
instructions](../docs/install-csi-driver.md#install-with-kubectl) instead of
uninstall/reinstall.

Keep `csidriver.name` equal to the existing driver/PV identity during an upgrade.
The chart now supplies it to the controller and nodes as well as Kubernetes
objects and the uninstall guard. If an older installation used explicit
`--drivername` arguments, align the chart setting with that existing identity;
matching arguments remain valid, but conflicting arguments fail rendering.
See [Driver Identity](../docs/driver-parameters.md#driver-identity). Changing
the name is not a volume-preserving migration.

> [!IMPORTANT]
> Node DaemonSets default to `RollingUpdate` with `maxUnavailable: 10%` per
> flavor DaemonSet, not across the whole cluster. Changed pod templates roll
> automatically. Compatible driver updates do not require draining workloads
> merely to replace CSI pods; supported resident Lustre modules can remain loaded.

Before upgrading, confirm the target driver and userspace support the resident
clients, and that the running loader preserves modules on termination. Older
loaders that unload modules, unqualified custom entrypoints, and incompatible
client transitions need planned maintenance with workloads stopped and Lustre
mounts removed. See [loader termination
behavior](../docs/csi-debug.md#teardown-behavior-sigterm-and-termination).

If the installation currently uses `OnDelete`, inspect its staged images,
client settings, and pod templates before changing strategy. Switching to
`RollingUpdate` can immediately replace pods with any pending template, not
just the changes in the next upgrade. Do not activate an incompatible staged
revision on mounted nodes.

    CHART_VERSION=0.7.0
    helm upgrade azurelustre --wait \
      oci://mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver \
      --namespace kube-system \
      --version "${CHART_VERSION}"

Or from local chart:

    helm upgrade azurelustre ./charts/latest/azurelustre-csi-driver --namespace kube-system

The built-in loader preserves resident modules on termination, including when
idle. A completed pod rollout therefore does not guarantee that a newer client
module is active. When a different module must become active, use a supported
node lifecycle operation and verify the loaded version. Do not use
`kubectl rollout restart` as a module-activation mechanism.

For deliberate template staging, set `--set node.updateStrategy.type=OnDelete`.
Existing pods then retain their current revision until replaced, while new
eligible pods use the staged template. A successful Helm upgrade in this mode
does not prove node pods converged. The chart omits `rollingUpdate` settings
for this explicit staging mode.

Existing `node.updateStrategy.maxUnavailable` overrides remain supported.
An explicit `node.updateStrategy.rollingUpdate.maxUnavailable` takes precedence.

### Cleanup-capable startup

The current source chart defaults to `node.cleanupCapableStartup=true` and
requires a matching driver image. CSI starts while Lustre packages are being
installed, keeping ordinary unmounts and already-published retries available.
New mounts require completed driver userspace setup, healthy LNet, and a
resident client supported by the projected compatibility policy for that flavor.
This client check applies even when optional image-policy enforcement is off.

The driver retries failed userspace setup without restarting its CSI endpoint.
After setup, it probes runtime every five seconds without further package
mutation. Successful observations are bound to the loaded client and policy
fingerprint and expire after 90 seconds, matching the node-fact freshness bound.
Missing, failed, changed, or expired evidence denies new mounts.

A restarting loader alone does not deny new mounts when the driver has fresh,
supported host-runtime evidence. Node startup taint removal uses that same
runtime gate. The Pod can still be NotReady because loader readiness contributes
to the Pod condition and rollout progress. Optional image admission retains
its current container-identity and digest checks.

CSI liveness remains independent so a repository outage does not kill a working
cleanup service. The loader still uses Kubernetes-managed restarts for failed
setup. Ordinary package recovery preserves resident Lustre/LNet modules and
their packages, including idle or non-current clients. It removes conflicting
packages only when no Lustre/LNet modules remain; package failure does not
authorize module activation.
Custom entrypoints must preserve the startup-state handoff; userspace retries
use the installer bundled in the image.

Use `--set node.cleanupCapableStartup=false` only to retain legacy startup.
That mode waits for loader readiness and installs driver utilities before
opening CSI, so package failures can block cleanup.

This is not module activation or evidence of extension-platform auto-upgrade.
It does not change the rolling-update budget, unmount behavior, or explicit
retirement semantics. Qualify the matching images and fault/recovery behavior
before deploying a source build to production.

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

For an in-place upgrade that preserves PVCs/PVs, use [Upgrade](#upgrade) instead.

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
  Alternatively, apply `preDeleteGuard.enabled=false` to the installed release
  before uninstalling, following the [upgrade precautions](#upgrade).
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
| `statusController.extraArgs` | Extra args passed only to the node status controller | `["-v=5"]` |
| `node.priorityClassName` | Node pod priority class | `system-node-critical` |
| `node.tolerations` | Node DaemonSet tolerations. Tolerates common AKS user-pool taints (spot, GPU) but **not** `CriticalAddonsOnly`, so the plugin stays off reserved/tainted system pools. Overriding **replaces** this set — for custom-tainted Lustre pools, include the spot/GPU entries you still need. | spot + `sku=gpu` + `nvidia.com/gpu` |
| `node.updateStrategy.type` | Node DaemonSet update strategy. Explicit `OnDelete` stages changes without replacing existing node pods. | `RollingUpdate` |
| `node.updateStrategy.maxUnavailable` | Backward-compatible budget per flavor DaemonSet during `RollingUpdate`; used unless the structured override is set | `10%` |
| `node.updateStrategy.rollingUpdate.maxUnavailable` | Explicit budget override; takes precedence over `node.updateStrategy.maxUnavailable` | unset |
| `node.jammy.lustreClient.version` | Lustre client version for jammy flavor | `2.15.8` |
| `node.jammy.lustreClient.shaSuffix` | Lustre client SHA suffix for jammy flavor | `39-g2d32b59` |
| `node.jammy.lustreClient.compatible` | Additional jammy clients allowed during rollout or rollback | `[]` |
| `node.noble.lustreClient.version` | Lustre client version for noble flavor | `2.17.0` |
| `node.noble.lustreClient.shaSuffix` | Lustre client SHA suffix for noble flavor | `24-gf517bc4` |
| `node.noble.lustreClient.compatible` | Additional noble clients allowed during rollout or rollback | `[]` |
| `node.azurelinux3.lustreClient.version` | Lustre client version for azurelinux3 flavor | `2.17.0` |
| `node.azurelinux3.lustreClient.shaSuffix` | Lustre client SHA suffix for azurelinux3 flavor | `24-gf517bc4` |
| `node.azurelinux3.lustreClient.compatible` | Additional Azure Linux 3 clients allowed during rollout or rollback | `[]` |
| `node.extraArgs` | Extra args passed to node driver | `["-v=5"]` |
| `compatibilityPolicy.clientSetRevision` | Monotonic revision of the desired/compatible client set | `1` |
| `compatibilityPolicy.cacheTTLSeconds` | Maximum controller use of cached policy during API read failures | `3600` |
| `compatibilityPolicy.mountAdmission.capabilityEnabled` | Enable node-side admission capability through the pod template; wait for capable pods before enforcing | `false` |
| `compatibilityPolicy.mountAdmission.enforce` | Dynamically gate genuinely new mounts after capable nodes are activated | `false` |
| `compatibilityPolicy.images.driver.<flavor>` | Desired and approved immutable driver digests for `jammy`, `noble`, or `azurelinux3` | empty |
| `compatibilityPolicy.images.loader.<flavor>` | Desired and approved immutable loader digests for the same OS flavors | empty |
| `compatibilityPolicy.bootstrap` | Finite previous-image exception for pre-reporter node pods | disabled |
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

The chart also deploys two replicas of the dedicated
`csi-azurelustre-status-controller`. It writes `AzureLustreNodeStatus`
resources and uses Lease-based leader election. To enable admission safely:

1. Configure desired and approved immutable image digests, compatible clients,
   and `mountAdmission.capabilityEnabled: true` while leaving `enforce: false`.
2. Activate the staged node DaemonSet revision through the intended node
   lifecycle and verify current, allowed `AzureLustreNodeStatus` resources.
3. Set `mountAdmission.enforce: true`; capable node pods observe the projected
   policy update without restarting.

Before enabling enforcement, account for its fail-closed availability behavior:
API-server access failures or stale controller decisions/node facts deny new
mounts, including remounts after a reboot. Already-mounted retries and cleanup
remain ungated. Controller readiness alone does not prove fresh decisions; see
[status controller availability and permissions](#status-controller-availability-and-permissions).

To intentionally disable enforcement, set `mountAdmission.enforce: false` in
the valid policy; **do not delete the compatibility-policy ConfigMap**. Once a
node has the capability enabled, a missing or malformed projected policy denies
new mounts, even if enforcement was previously disabled. Restore a valid policy
to recover. The disable takes effect as each node receives the projected update,
not instantaneously.

Any policy edit can temporarily deny new mounts with `PolicyChanged` while the
controller's decision and the node's projected file have different fingerprints.
Wait for projection and fresh matching decisions before relying on the change;
do not bypass the fingerprint check to avoid this convergence window.

Helm installs the CRD from `crds/`, but does not upgrade existing CRDs; apply the
updated CRD manifest explicitly before upgrading to a chart that changes its
schema. Uninstall intentionally retains the CRD; status resources in namespaces
that remain also require explicit cleanup if no longer needed.

New node-fact Leases are owned by the Node UID, not the reporting Pod. Pod
replacement renews the same fact; Kubernetes garbage collection removes it
when that Node is deleted. Renewal preserves existing ownership so heartbeat
publication does not depend on the Lease-delete permission that some admission
configurations require for ownership changes.

Facts from older development builds may lack Node ownership and will not be
garbage-collected with the Node until explicitly migrated. An authorized
operator must inspect each fact's node-fact labels and Node UID annotation,
verify the matching live Node incarnation, and add its non-blocking Node owner
reference. Alternatively, stop the reporter and delete only the identified
fact so the new reporter recreates it with ownership. Already-orphaned facts
need explicit, scoped cleanup.

Uninstall from a still-live Node retains its fact until Node deletion, if it
has the matching Node owner, or explicit cleanup after the reporter has stopped.
Inspect the labels, Node UID annotation and owner reference before deleting
only the identified fact; never delete all Leases or the shared driver namespace.

### Per-flavor image approval

The unpublished `azurelustre.csi.azure.com/v1alpha1` compatibility policy uses
the same map shape for both roles:
`images.<driver|loader>.<jammy|noble|azurelinux3>.{desiredDigest,approvedDigests}`.
The former global `images.driver.desiredDigest` / `approvedDigests` shape is
rejected by both the policy parser and Helm values schema; there is no global
fallback. Refresh development smoke chart copies and driver binaries together.
Older admission-capable binaries cannot parse the new shape and fail closed
for new mounts until safely replaced; already-mounted retries and cleanup
remain ungated.

Each node DaemonSet uses its OS-specific image for **both** the driver and
loader. Populate each role from its actual immutable runtime image digest.
For the normal same-image deployment, the driver and loader digests for one
flavor are equal, but Jammy and Noble digests are not interchangeable. With
enforcement enabled, every configured flavor needs both role policies, and
each desired digest must appear in its corresponding approved list. Prior
digests may remain approved **only for the same flavor and role**.

At least one flavor must be configured to enforce admission. A flavor with
both role policies empty or absent is deliberately unconfigured: its image
evidence is `Unknown` and new mounts are denied, without invalidating configured
flavors. A partially configured pair or malformed digest invalidates the policy.
For example, a Jammy/Noble smoke run can leave Azure Linux 3's two default image
policies empty; never populate them with another OS's digest.

Use these Helm values after replacing `${JAMMY_DIGEST}` and `${NOBLE_DIGEST}`
with the actual canonical `sha256:<64 hex characters>` digests, not the literal
placeholder strings. The resulting ConfigMap embeds the same `images` object
under the policy root:

    compatibilityPolicy:
      schemaVersion: v1alpha1
      mountAdmission:
        capabilityEnabled: true
        enforce: true
      images:
        driver:
          jammy:
            desiredDigest: "${JAMMY_DIGEST}"
            approvedDigests: ["${JAMMY_DIGEST}"]
          noble:
            desiredDigest: "${NOBLE_DIGEST}"
            approvedDigests: ["${NOBLE_DIGEST}"]
        loader:
          jammy:
            desiredDigest: "${JAMMY_DIGEST}"
            approvedDigests: ["${JAMMY_DIGEST}"]
          noble:
            desiredDigest: "${NOBLE_DIGEST}"
            approvedDigests: ["${NOBLE_DIGEST}"]

Client compatibility remains independently configured through
`node.<flavor>.lustreClient`; use the actual loaded client identities for the
images under test. Both matching flavors can report `securityCompliance:
Approved` concurrently. Approved older images report `NonCurrentApproved`.
`csiDelivery: Current` separately means the pod matches its own desired
DaemonSet revision. Inspect `desired.flavor`, `desired.driverImageDigest`, and
`desired.loaderImageDigest` alongside the observed digest facts.

The controller binds reporter flavor to the current Pod's `flavor` label, and
node admission rechecks the same flavor's image approvals against current
runtime evidence. A digest approved only for another OS cannot grant admission.
This binds cooperative facts to Kubernetes pod identity, not kernel attestation.

### Status controller availability and permissions

Both replicas serve `/healthz` and `/readyz` on TCP port `29654`. Followers
remain Ready so the PDB can protect an available successor. The probes check
the election watchdog and, on the leader, reconciliation progress; they do
not assert that every node has a fresh or allowed mount decision. SIGTERM
cancels election and reconciliation, with a bounded ten-second shutdown.
Unexpected leadership loss or a failed health server exits with an error.
The lock is not released early: a successor waits for the thirty-second
lease to expire, plus acquisition/reconciliation time. This avoids handing
off while cancelled status writes might still be in flight.

Election uses a chart-owned, **exclusive control namespace**, separate from
the driver namespace where node facts live. Its stable name is
`azurelustre-control-<32 hexadecimal characters>`, derived from SHA-256 of
the complete `<release namespace>/<release name>` identity, without namespace
truncation. Only the status-controller service account is bound to election
write permissions there. The controller can only read node-fact Leases; node
service accounts cannot write the election lock through this chart's RBAC.
Administrators must not add node permissions or other workloads to the control
namespace. Node facts remain cooperative reporting, **not attestation** of
the loaded kernel state; all node pods still share the node-fact writer role.

The installer must be able to create/read Namespaces and manage their RBAC.
The chart refuses to adopt an existing control namespace lacking matching
Helm release ownership and the election-component label, including when
`--take-ownership` is requested. `helm template` cannot inspect live ownership;
use Helm install/upgrade or server-side dry run for that check. The namespace
has no `keep` annotation and is deleted on uninstall, including any resources
placed in it. Do not share it, rename the release as a migration mechanism, or
manually adopt a conflicting namespace. With `rbac.create=false`, the namespace
is still chart-owned, but an administrator must provision the rendered RBAC.

**One-time migration from the old driver-namespace election lock:** stop all
old status-controller pods before upgrading, and do the same before a rollback
to the old lock layout. Do not roll old and new lock domains concurrently:
each could elect its own writer. Scale only the status-controller Deployment
to zero and wait for its pods to disappear, then apply the new chart (which
restores two replicas). Do not restart node DaemonSets. Expect a pause in status
updates; new-mount admission fails closed if evidence expires, while existing
mount retries and cleanup remain ungated. Subsequent upgrades within the new
lock layout can use the normal Deployment rollout.

For static manifests, first **create**, rather than adopt or overwrite,
`deploy/namespace-csi-azurelustre-status-controller.yaml`, then apply
`deploy/rbac-csi-azurelustre-status-controller.yaml` before the Deployment.
The static namespace corresponds to release `azurelustre` in `kube-system`.
Static resources are not Helm-owned and cannot be silently adopted by Helm.
`deploy/install-driver.sh local` performs that ownership check and stops old
status-controller pods automatically when it detects a different lock layout.
It reuses only a namespace marked `app.kubernetes.io/managed-by:
azurelustre-static` with the election-component label and no Helm owner.
On static uninstall, stop/delete the status-controller Deployment before
deleting its dedicated namespace; `deploy/uninstall-driver.sh` enforces that
ordering and the namespace ownership check. Never delete the driver namespace
as cleanup.

### Reconciliation under load

Each leader sweep has a sixty-second deadline and eight workers. Existing
status objects are LISTed once, with resource versions reused for status
writes; conflicts trigger a fresh GET and bounded retries. New objects may
require create and a GET if another writer won the creation race. Each node's
write errors remain independent. Canonical status names equal node names,
and garbage collection retains UID/resourceVersion delete preconditions.

An in-memory cursor resumes after the last attempted job, including failures,
instead of restarting at the same sorted prefix on every bounded sweep.
Garbage collection shares that fair queue. The cursor survives membership
changes but resets on process restart or leadership transfer; this is not a
durable work queue. Slow/API-limited fleets can still exceed freshness budgets
and deny new mounts safely. Unit tests check action counts with a 1,000-node
fixture and bounded-sweep fairness, not a supported cluster-size limit or live
HA/scalability evidence. Validate real API latency, renewal failure and
takeover, status age, and mount behavior in the target environment.

For development details see repository root `README.md` and docs in `docs/`.
