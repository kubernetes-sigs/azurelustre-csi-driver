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
| `statusController.extraArgs` | Extra args passed only to the node status controller | `["-v=5"]` |
| `node.priorityClassName` | Node pod priority class | `system-node-critical` |
| `node.tolerations` | Node DaemonSet tolerations. Tolerates common AKS user-pool taints (spot, GPU) but **not** `CriticalAddonsOnly`, so the plugin stays off reserved/tainted system pools. Overriding **replaces** this set — for custom-tainted Lustre pools, include the spot/GPU entries you still need. | spot + `sku=gpu` + `nvidia.com/gpu` |
| `node.updateStrategy.type` | Node DaemonSet update strategy. `OnDelete` stages changes without replacing existing node pods. | `OnDelete` |
| `node.updateStrategy.rollingUpdate.maxUnavailable` | Maximum unavailable node pods when explicitly using `RollingUpdate` | `10%` |
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
| `compatibilityPolicy.mountAdmission.capabilityEnabled` | Stage node-side admission capability through the `OnDelete` pod template | `false` |
| `compatibilityPolicy.mountAdmission.enforce` | Dynamically gate genuinely new mounts after capable nodes are activated | `false` |
| `compatibilityPolicy.images.driver.<flavor>` | Desired and approved immutable driver digests for `jammy`, `noble`, or `azurelinux3` | empty |
| `compatibilityPolicy.images.loader.<flavor>` | Desired and approved immutable loader digests for the same OS flavors | empty |
| `compatibilityPolicy.bootstrap` | Finite previous-image exception for pre-reporter node pods | disabled |
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
