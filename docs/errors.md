# Azure Lustre CSI Driver - Resolving Common Errors

This document describes common errors that can occur during driver startup and shutdown, volume creation, mounting, and driver uninstall, along with debugging and troubleshooting steps.

Examples use `kube-system` and static-manifest workload names. For Helm installs,
substitute your release namespace and the workload names returned by:

```bash
kubectl get pods,deployments,daemonsets -A -l app.kubernetes.io/name=azurelustre-csi-driver
```

## Table of Contents

- [Driver Lifecycle Errors](#driver-lifecycle-errors)
- [Driver Uninstall Errors](#driver-uninstall-errors)
- [Volume Creation Errors](#volume-creation-errors)
  - [Dynamic Provisioning Errors](#dynamic-provisioning-errors)
    - [Authentication and Authorization Errors](#authentication-and-authorization-errors)
    - [Error: Lease access is forbidden](#error-lease-access-is-forbidden)
    - [Error: Incomplete Azure API response](#error-incomplete-azure-api-response)
    - [Error: Uncertain Azure creation outcome](#error-uncertain-azure-creation-outcome)
    - [Error: AMLFS cluster creation timed out](#error-amlfs-cluster-creation-timed-out)
    - [Error: AMLFS provisioning failed](#error-amlfs-provisioning-failed)
    - [Deleted pending PVC or untracked filesystem](#deleted-pending-pvc-or-untracked-filesystem)
    - [Error: Resource not found](#error-resource-not-found)
    - [Error: Cannot create AMLFS cluster, not enough IP addresses available](#error-cannot-create-amlfs-cluster-not-enough-ip-addresses-available)
    - [Error: Reached Azure Subscription Quota Limit for AMLFS Clusters](#error-reached-azure-subscription-quota-limit-for-amlfs-clusters)
- [Pod Scheduling Errors](#pod-scheduling-errors)
  - [Node Readiness and Taint Errors](#node-readiness-and-taint-errors)
    - [Error: Node had taint azurelustre.csi.azure.com/agent-not-ready](#error-node-had-taint-azurelustrecsiazurecomagent-not-ready)
- [Volume Mounting Errors](#volume-mounting-errors)
  - [Node Mount Errors](#node-mount-errors)
    - [Error: Could not mount target](#error-could-not-mount-target)
    - [Error: MGS IP address is not reachable](#error-mgs-ip-address-is-not-reachable)
    - [Error: Context sub-dir must be strict subpath](#error-context-sub-dir-must-be-strict-subpath)
    - [Error: Subdirectory contains a symlink](#error-subdirectory-contains-a-symlink)
- [Configuration Errors](#configuration-errors)
  - [StorageClass Parameter Errors](#storageclass-parameter-errors)
    - [Error: Cannot unmarshal number](#error-cannot-unmarshal-number)
    - [Error: CreateVolume Parameter zone must be provided for dynamically provisioned AMLFS](#error-createvolume-parameter-zone-must-be-provided-for-dynamically-provisioned-amlfs)
    - [Error: CreateVolume Parameter zone cannot be used in location, no zones available for SKU](#error-createvolume-parameter-zone-cannot-be-used-in-location-no-zones-available-for-sku)
    - [Error: CreateVolume Parameter zone must be one of available zones](#error-createvolume-parameter-zone-must-be-one-of-available-zones)
    - [Error: CreateVolume Parameter sku-name must be one of supported values](#error-createvolume-parameter-sku-name-must-be-one-of-supported-values)
    - [Warning: SKU retrieval failures](#warning-sku-retrieval-failures)
    - [Error: CreateVolume Parameter maintenance-time-of-day-utc must be in HH:MM format](#error-createvolume-parameter-maintenance-time-of-day-utc-must-be-in-hhmm-format)
  - [Kubernetes resource quota restriction](#kubernetes-resource-quota-restriction)
    - [Error: Dynamic provisioning would exceed default quota](#error-dynamic-provisioning-would-exceed-default-quota)
- [Debugging and Troubleshooting](#debugging-and-troubleshooting)
  - [Log Collection](#log-collection)
    - [Controller Logs](#controller-logs)
    - [Node Logs](#node-logs)
    - [Comprehensive Log Collection](#comprehensive-log-collection)

---

## Driver Lifecycle Errors

Check `azurelustre` logs for startup, serving, and cleanup failures, and
client logs (such as `csi-provisioner`) for gRPC statuses. Use the
[startup and shutdown troubleshooting steps](csi-debug.md#driver-startup-and-shutdown-failures)
to collect current and previous logs, termination details, and events.

| Message or gRPC status | Meaning | What to check |
| ---------------------- | ------- | ------------- |
| `failed to initialize Azure Lustre CSI driver` | Driver construction failed. | Read the accompanying error for the specific configuration, identity, or local initialization failure. |
| `invalid endpoint`, `failed to prepare Unix socket <path>`, or `failed to listen` | The CSI endpoint could not be prepared. | Check `--endpoint`, the underlying OS error, socket-directory mounts and permissions, and address conflicts. Do not remove a socket belonging to a running driver. |
| `refusing to remove endpoint that is not a socket` | A file, directory, or symlink occupies the configured Unix endpoint. Startup leaves it untouched. | Correct the endpoint or resolve the unexpected object after establishing its ownership. The driver only removes socket objects; scheme names are case-insensitive. |
| `failed to serve on <address>` | gRPC serving failed. | Inspect the underlying listener error and correlate it with container restarts or pod termination. |
| `timed out shutting down gRPC server` | The driver's ten-second cleanup deadline expired. Remaining gRPC connections are closed and the process exits with an error. | Capture the driver logs and in-flight request details. This is not an Azure filesystem provisioning timeout; extending the pod termination grace period does not change this driver deadline. |
| `Unavailable` with `gRPC server is shutting down` | A new request was rejected during shutdown. | Check whether termination was planned and whether a replacement driver is healthy. Other `Unavailable` messages can have different causes. |
| `Canceled` / `context canceled` | The caller or driver shutdown canceled a request. | Correlate driver and client logs with termination events. This does not establish that an Azure operation failed or was canceled. |
| `driver.Run returned unexpectedly` | Serving returned without cancellation of the executable's context. | Collect driver logs and termination details and report the unexpected exit. |

A completed signal-driven shutdown exits successfully; see the
[shutdown contract](../README.md#driver-shutdown). Cancellation stops local
polling, not an operation already accepted by Azure. Do not delete or recreate
a PVC or AMLFS resource solely because one of these messages appeared. Check
the resource's actual state and ownership before considering recovery actions;
deleting a PVC with a `Delete` reclaim policy can delete the filesystem and
its data.

---

## Driver Uninstall Errors

For `failed pre-delete` hook errors, matching-PV rejections, or a Helm release
left in `uninstalling`, use the [blocked-uninstall
procedure](../charts/README.md#troubleshoot-a-blocked-uninstall). Capture diagnostics
before retrying: failed Jobs expire 300 seconds after completion by default,
and pod logs can disappear sooner.

Follow the [safe teardown procedure](../charts/README.md#safe-teardown-procedure)
before deleting claims; the `Delete` reclaim policy can destroy backing data.

---

## Volume Creation Errors

### Dynamic Provisioning Errors

#### Authentication and Authorization Errors

**Symptoms:**

- Controller logs show 403 `Forbidden` or 401 `Unauthorized` HTTP errors
- AMLFS cluster creation fails with permission errors

**Possible Causes:**

- Insufficient Azure RBAC permissions for the identity used by the controller
- Incorrect identity configuration
- Workload identity is enabled but the federated credential subject does not match the controller ServiceAccount

**Debugging Steps:**

```bash
# Check controller logs for auth errors
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "forbidden\|unauthorized\|authorization"

# Verify the controller identity's permissions in Azure portal
# Check: IAM (Access control) → Role assignments
```

**Resolution:**

- Assign required resource-scoped RBAC roles to the identity used by the controller:
  the kubelet identity by default, or the federated user-assigned identity when
  workload identity is enabled.
  - See [Permissions For Kubelet Identity](driver-parameters.md#permissions-for-kubelet-identity)
- Permission denials from SKU lookup, subnet sizing or VNet usage are discovery
  warnings, not provisioning failures. Grant the
  [optional fail-fast permissions](driver-parameters.md#optional-fail-fast-checks)
  only if you want those early checks.
- If using workload identity, confirm the federated credential subject matches the controller ServiceAccount. It must be `system:serviceaccount:<namespace>:csi-azurelustre-controller-sa`. The chart annotates that ServiceAccount automatically, so a missing `azure.workload.identity/client-id` annotation means the release was not installed with `IsWorkloadIdentityEnabled=Enabled`. See [Workload Identity](workload-identity.md).

---

#### Error: Lease access is forbidden

If `csi-provisioner` logs report `leases.coordination.k8s.io` access is
`forbidden`, leader election is blocked by Kubernetes RBAC, not Azure role
assignments.

```bash
kubectl logs -n <driver-namespace> -l app=csi-azurelustre-controller -c csi-provisioner --tail=100 --prefix
kubectl get role,rolebinding -n <driver-namespace> -l app.kubernetes.io/name=azurelustre-csi-driver -o yaml
```

Check that the Role permits Lease access and its RoleBinding names
`csi-azurelustre-controller-sa` in the controller's namespace. Restore the
chart's namespaced RBAC rather than granting cluster-wide Lease access.

---

#### Error: Incomplete Azure API response

Provisioning reports `Unavailable` with `received incomplete AMLFS response` or
`received incomplete subnet usage response`.
The message identifies the affected resource or location and missing information.

Missing subnet-usage data prevents the driver from checking capacity; it does not
mean the subnet is full. A missing MGS address does not prove creation failed:
the filesystem may already exist, and this error does not trigger its deletion.

SKU lookup warnings do not by themselves prevent provisioning. See
[SKU retrieval failures](#warning-sku-retrieval-failures).

Keep the PVC and driver in place while provisioning retries. If the error
persists, collect the
[controller logs](#controller-logs), check the affected resource in Azure, and
contact Azure support with the missing-field message and resource details. Do not
delete the PVC or filesystem solely because of this error.

---

#### Error: Uncertain Azure creation outcome

`Unavailable` can mean the driver could not determine the outcome of Azure
work, including a failed status lookup, an error polling accepted creation, or
blocked recovery of a matching `Failed` filesystem. Messages include:

- `AMLFS operation outcome is unresolved`
- `Failed recovery is blocked by admission`
- `AMLFS creation outcome is uncertain`

Keep the pending PVC and driver in place while retries check Azure state.
Correct the reported problem: for example, a 401/403 while checking an existing
filesystem requires restoring authentication or the controller's required
permissions. Retrying does not grant access, and a failed status check does not
prove that Azure creation or deletion stopped.

The error includes details from the current failed attempt when available. It
does not guarantee that an earlier service failure remains visible after a
timeout or restart. Use the [Azure Activity Log](#error-amlfs-provisioning-failed)
when the original cause is missing.

Definitive rejection of a new creation request is still reported as an error.
A confirmed incompatible filesystem returns `AlreadyExists` and is left
unchanged; retries do not overwrite it.

---

#### Error: AMLFS cluster creation timed out

A `DeadlineExceeded` or `create did not complete` message does not prove that
Azure stopped creating the filesystem. Creation can take longer than one
request, and the driver checks Azure state again on retry.

1. Keep the pending PVC and driver in place. Check
   [PVC events and controller logs](csi-debug.md#dynamic-provisioning-amlfs-cluster-creation).
2. Check the filesystem in Azure. If it is still `Creating`, allow provisioning
   to continue. If it is `Failed` or repeatedly recreated, follow
   [AMLFS provisioning failed](#error-amlfs-provisioning-failed).
3. Correct any reported connectivity, permission, or service issue. A timeout
   alone is not a reason to delete the PVC or filesystem, or to start another
   request in a different region.

---

#### Error: AMLFS provisioning failed

During unfinished provisioning, the driver replaces a matching `Failed`
filesystem after its deletion completes. Recovery can repeat if the underlying
problem persists; retries do not repair network configuration or service issues.

PVC events and controller logs may show only `Failed` or a generic health
message rather than the original service failure, particularly after a timeout
or controller restart. To investigate:

1. Note the filesystem name/resource ID and the failure time from the controller
   logs or PVC events.
2. Open the filesystem's resource group in the Azure portal and select
   **Activity log**. Set the time range to include the attempt and find the
   failed create/update event for that filesystem.
3. Read the event's JSON, including `statusMessage`, for the service error code
   and details. The resource-group log also covers a filesystem that the driver
   has already deleted or recreated. See
   [View and retrieve the activity log](https://learn.microsoft.com/en-us/azure/azure-monitor/fundamentals/activity-log#view-and-retrieve-the-activity-log).
4. Address the reported cause. For networking failures, check
   [AMLFS network prerequisites](https://learn.microsoft.com/en-us/azure/azure-managed-lustre/amlfs-prerequisites#network-prerequisites),
   including outbound access from the filesystem subnet. For other failures, use
   [AMLFS deployment troubleshooting](https://learn.microsoft.com/en-us/azure/azure-managed-lustre/troubleshoot-deployment)
   or contact Azure support with the resource ID, time, and correlation ID.

Keep the claim and driver in place while correcting the problem. Do not use the
unfinished filesystem for data: recovery may replace it.

---

#### Deleted pending PVC or untracked filesystem

Deleting a pending PVC or its namespace does not cancel an Azure creation
request. The provisioner may still finish creating a PV and apply its reclaim
policy, but this is not guaranteed. If the claim is gone and the controller loses
the unfinished request before the PV is saved, a filesystem can remain billable
without a Kubernetes volume. A controller restart while the PVC still exists
can resume provisioning; deleting the claim and also losing the unfinished
request is the case which can leave behind an untracked cluster.

After an interrupted provisioning attempt whose claim was deleted:

1. Keep the driver available and inspect controller logs and the Azure Activity
   Log. Use the StorageClass's `resource-group-name`, or the AKS infrastructure
   resource group when that parameter was omitted, to inventory filesystems:

   ```shell
   az amlfs list --subscription <subscription-id> --resource-group <filesystem-resource-group> --output json
   kubectl get pv -o json
   kubectl get pvc --all-namespaces -o json
   ```

2. Compare full filesystem resource IDs with PV mappings and PVC UIDs. With the
   default provisioner settings, the filesystem name is `pvc-<PVC-UID>`.
   Azure tags such as `kubernetes.io-created-for-pvc-name` help identify a request,
   but a newly created PVC with the same name has a different UID.
3. Treat an unmatched filesystem as a candidate for investigation, not automatic
   deletion. Confirm ownership, current Azure operation state, any deliberate
   `Retain` decision, and use by other clusters or static mounts. Preserve data
   that is still needed. Missing Kubernetes objects or matching tags alone do
   not establish that deletion is safe.

Keep the driver installed until tracked provisioning and deletion have finished.
Do not remove PV finalizers to force cleanup.

---

#### Error: Resource not found

Check the Azure error code and affected resource, not just words in the message.
A failed lookup of a configured resource group or network is not evidence that
an AMLFS filesystem is absent. Address the reported configuration or permission
error rather than assuming no Azure work remains.

**Symptoms:**

- Controller logs show one or more of the following:
  - `Resource group 'myapp-rg/myapp-vnet-rg' could not be found.`
  - `The Resource '.../myapp-vnet' under resource group 'myapp-rg' was not found.`
  - `subnet ../myapp-subnet not found in vnet myapp-vnet, resource group myapp-rg. Ensure permissions are correct for configuration`
- The status depends on the operation:
  - Failed filesystem observation or polling after accepted creation remains
    retryable, normally as `Unavailable`. See
    [Uncertain Azure creation outcome](#error-uncertain-azure-creation-outcome).
  - New-create or subnet-check errors can report `NotFound`, `PermissionDenied`,
    or `Unauthenticated`.
  - `FailedPrecondition` means the requested subnet is absent from the returned
    usage inventory.

**Possible Causes:**

- Incorrect resource identity in StorageClass parameters
- Subnet doesn't exist in the specified virtual network
- Insufficient permissions to access the resource

**Debugging Steps:**

```bash
# Verify StorageClass parameters
kubectl get storageclass <storageclass-name> -o yaml

# Check controller logs for network-related errors
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "subnet\|vnet\|network"
```

**Resolution:**

- Verify subnet name and virtual network configuration
- Ensure the vnet and subnet exist in the correct resource group
- Check the required Azure RBAC permissions for the identity used by the controller

---

#### Error: Cannot create AMLFS cluster, not enough IP addresses available

**Symptoms:**

- Controller logs show: `cannot create AMLFS cluster myapp-lustre-cluster in subnet myapp-subnet, not enough IP addresses available`
- Error code: `ResourceExhausted`

**Possible Causes:**

- The specified subnet has insufficient available IP addresses

**Debugging Steps:**

```bash
# Check the subnet information
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "subnet"
```

- This should return output such as:

```text
There is not enough room in the /subscriptions/<sub-id>/resourceGroups/<rg>/providers/Microsoft.Network/virtualNetworks/<vnet>/subnets/<subnet> subnet to fit a AMLFS-Durable-Premium-40 SKU cluster: 10 needed, 3 available
```

- Verify subnet details in Azure portal
- Navigate to: Virtual Networks → [your-vnet] → Subnets → [your-subnet]

**Resolution:**

- Use a subnet with more available IP addresses
- Create a new dedicated subnet for AMLFS clusters
- Expand the existing subnet's address space if possible

---

#### Error: Reached Azure Subscription Quota Limit for AMLFS Clusters

**Symptoms:**

- Controller logs show messages such as: `Operation results in exceeding quota limits of resource type AmlFilesystem. Maximum allowed: 4, Current in use: 4, Additional requested: 1.`
- Error code: `ResourceExhausted`

**Possible Causes:**

- The total number of AMLFS clusters in your subscription has been reached
  - This limit includes all AMLFS clusters, including those manually created outside of this CSI driver

**Debugging Steps:**

```bash
# Check Azure portal for how many AMLFS clusters exist in your subscription (should match output of error)
```

**Resolution:**

- Delete any unneeded AMLFS clusters to free up quota
- Request an increase in this quota for your subscription if more are still needed

---

## Pod Scheduling Errors

### Node Readiness and Taint Errors

#### Error: Node had taint azurelustre.csi.azure.com/agent-not-ready

**Symptoms:**

- Pods requiring Azure Lustre storage remain stuck in `Pending` status
- Pod events show taint-related scheduling failures:
  - `Warning  FailedScheduling  ... node(s) had taint {azurelustre.csi.azure.com/agent-not-ready: }, that the pod didn't tolerate`
  - `0/X nodes are available: X node(s) had taint {azurelustre.csi.azure.com/agent-not-ready}`
- Kubectl describe pod shows scheduling failures due to taints

**Possible Causes:**

- CSI driver is still initializing on nodes
- Lustre kernel modules are not yet loaded (the `lustre-loader` startup sidecar is still bringing up LNet)
- The `azurelustre` driver is still installing its userspace tools, so its CSI socket is not yet serving
- CSI driver failed to start properly on affected nodes
- Node is not ready to handle Azure Lustre volume allocations
- CSI driver startup taint removal is disabled

**Debugging Steps:**

```bash
# Check pod scheduling status
kubectl describe pod <pod-name> | grep -A10 Events

# Check which nodes have the taint
kubectl describe nodes | grep -A5 -B5 "azurelustre.csi.azure.com/agent-not-ready"

# Verify CSI driver pod status on nodes
kubectl get pods -n kube-system -l app=csi-azurelustre-node -o wide

# Node pods are 4/4 when ready. If below 4/4, find which container is not ready:
# the lustre-loader sidecar (LNet) and/or the azurelustre driver (CSI socket).
kubectl get pod -n kube-system <pod-name> -o jsonpath='{range .status.initContainerStatuses[*]}{.name}={.ready}{"\n"}{end}{range .status.containerStatuses[*]}{.name}={.ready}{"\n"}{end}'

# Check the loader sidecar's LNet readiness directly
kubectl exec -n kube-system <pod-name> -c lustre-loader -- /app/readinessProbe.sh

# Check CSI driver startup logs (taint removal is performed by the driver container)
kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre --tail=100 | grep -i "taint\|ready\|error"

# Verify taint removal is enabled (should be true by default)
kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre | grep -i "remove.*taint"
```

**Resolution:**

1. **Wait for CSI Driver Readiness** (most common case):

   ```bash
   # Wait for CSI driver pods to reach Running status
   kubectl wait --for=condition=ready pod -l app=csi-azurelustre-node -n kube-system --timeout=300s
   ```

   The taint should be automatically removed once the CSI driver is fully operational.

2. **Check Lustre Module Loading**:

   ```bash
   # Verify Lustre modules are loaded on nodes (modules are loaded by the loader sidecar)
   kubectl exec -n kube-system <csi-azurelustre-node-pod> -c lustre-loader -- lsmod | grep lustre
   ```

3. **Verify CSI Driver Configuration**:

   ```bash
   # Print the driver's arguments. Taint removal is on by default, so the flag is
   # normally ABSENT; it is only disabled if you see --remove-not-ready-taint=false.
   # The node runs as per-OS-flavor DaemonSets; check the one for the affected
   # node's OS (jammy/noble/azurelinux3).
   kubectl get ds csi-azurelustre-node-jammy -n kube-system \
     -o jsonpath='{.spec.template.spec.containers[?(@.name=="azurelustre")].args}'
   ```

4. **Emergency Manual Taint Removal** (not recommended for production):

   ```bash
   # Only use if CSI driver is confirmed working but taint persists
   kubectl taint nodes <node-name> azurelustre.csi.azure.com/agent-not-ready:NoSchedule-
   ```

**Prevention:**

- Ensure CSI driver has sufficient time to initialize during cluster updates
- Monitor CSI driver health during node scaling operations
- Use pod disruption budgets to prevent scheduling issues during maintenance

---

## Volume Mounting Errors

### Node Mount Errors

#### Error: Could not mount target

`node mount services are not initialized on this driver instance` returns
`Unavailable` without mounting or unmounting. Check that kubelet is connected to
the node plugin and inspect its startup logs; the controller does not initialize
host mount services.

`node path ... must be an absolute, non-root path without NUL characters` returns
`InvalidArgument` before publish or unpublish filesystem work. CSI
callers must provide a valid absolute target. Custom kubelet root directories are
supported; this validation does not impose a fixed host directory allowlist.

If the driver cannot determine whether a target is mounted, cannot unmount an
existing target, or cannot remove a target after a failed mount, it returns
`Unavailable`. Kubelet keeps the mount state uncertain so it can retry or clean up
the volume when the Pod is removed. This error is not proof that the volume is
unmounted. A failed mount whose target is confirmed removed or absent can return
`Internal` instead.

Keep the driver running, inspect the node logs for the original mount error and
any cleanup error, and correct the reported node or filesystem problem. Do not
remove kubelet volume metadata or force-delete storage objects to hide the error.

For `temporary mount cleanup incomplete`, the driver could not finish removing
the temporary mount used to create a subdirectory. The publish call does not
report success. A later publish retries that cleanup before target preparation or
the MGS probe, and unpublishing also checks for a leftover temporary mount. This
keeps a later probe or preparation failure from hiding unfinished cleanup. An
already-mounted public target still does not require an MGS probe.
The message includes both errors if subdirectory creation and cleanup failed.
Unmounting preserves the filesystem's data; it does not delete the subdirectory.

**Symptoms:**

- Pod fails to start with mount errors
- Mount operations hang or timeout
- Node logs show:
  - `Could not mount "10.10.10.10@tcp:/lustrefs" at ".../volumes/kubernetes.io~csi/<pvc-id>/mount": mount failed`
  - `Input/output error`
  - `Is the MGS running?`
- Network connectivity tests fail to MGS IP
- Pods stuck in `ContainerCreating` status

**Possible Causes:**

- Virtual network peering issues
- Network security group blocking AMLFS traffic
- Incorrect MGS IP address configuration when using static provisioning
- Incorrect network configuration between Kubernetes cluster and AMLFS cluster
- Firewall blocking required ports
- Network connectivity issues to AMLFS cluster
- Incorrect mount options or parameters

**Debugging Steps:**

```bash
# Check node logs for mount details
kubectl logs -n kube-system csi-azurelustre-node-<pod> -c azurelustre --tail=300 | grep -i mount

# Check network security group rules

# Verify MGS IP in volume configuration
kubectl get pv <pv-name> -o jsonpath='{.spec.csi.volumeAttributes.mgs-ip-address}'
```

**Resolution:**

- Check virtual network peering configuration
- Verify network connectivity to AMLFS filesystem
- Validate MGS IP address is correct and reachable
- Check firewall rules and network security groups
- Add NSG rules to allow AMLFS traffic if necessary

---

#### Error: MGS IP address is not reachable

**Symptoms:**

- Pod fails to start and stays in `ContainerCreating` status
- Before mounting, the driver checks reachability in two stages: a fast TCP dial to the LNet acceptor port (default `988`), then an `lnetctl ping`. A wrong or unreachable MGS IP fails the dial within about 5 seconds. If a port is open but the peer is not a healthy LNet endpoint, the `lnetctl ping` stage can take up to about 50 seconds before failing (the LNet handshake is an in-kernel wait that cannot be shortened).
- `NodePublishVolume` fails with:
  - `MGS IP address "10.10.10.10" is not reachable; verify the cluster IP address and node network configuration are correct (if the cluster was only briefly unreachable, the operation will recover on retry)`
- Error code: `FailedPrecondition`
- Node logs show either `LNet reachability gate: dial to <mgs-ip>:988 failed` or `lnetctl ping to <mgs-ip>@tcp failed with error`

**Possible Causes:**

- Incorrect MGS IP address in the PersistentVolume or StorageClass
- Missing or misconfigured virtual network peering or routing between the cluster and the AMLFS cluster
- Network security group or firewall blocking LNet (TCP) traffic to the MGS
- The AMLFS cluster is stopped, deleting, or otherwise temporarily unreachable
- LNet is not configured correctly on the node (see [CSI Driver Troubleshooting Guide](csi-debug.md))

**Debugging Steps:**

```bash
# Verify the MGS IP recorded on the volume
kubectl get pv <pv-name> -o jsonpath='{.spec.csi.volumeAttributes.mgs-ip-address}'

# Look for the reachability failure in the node driver logs
kubectl logs -n kube-system csi-azurelustre-node-<pod> -c azurelustre --tail=300 | grep -iE 'reachability|ping'

# Reproduce the reachability check from the node pod
kubectl exec -n kube-system csi-azurelustre-node-<pod> -c azurelustre -- lnetctl ping <mgs-ip>@tcp
```

**Resolution:**

- Correct the MGS IP address in the PersistentVolume or StorageClass if it is wrong
- Verify virtual network peering and routing between the cluster and the AMLFS cluster
- Add NSG or firewall rules to allow LNet (TCP) traffic to the MGS
- Confirm the AMLFS cluster is running and healthy in the Azure portal
- The check result is cached briefly (about 20 seconds); once connectivity is restored, the operation will recover on retry

---

#### Error: Context sub-dir must be strict subpath

**Symptoms:**

- Mount fails when using subdirectories
- Node logs show: `Context sub-dir must be strict subpath`
- Error code: `InvalidArgument`

**Possible Causes:**

- Subdirectory path contains invalid characters or patterns
- Attempted directory traversal (e.g., `../` in path)
- Malformed subdirectory template variables

**Debugging Steps:**

- Check template variables in subdirectory are valid and would be parsed as a valid subpath

```bash
# Check subdirectory configuration in PV or StorageClass
kubectl get pv <pv-name> -o yaml | grep sub-dir

# Check node logs for sub-dir issue description
kubectl logs -n kube-system csi-azurelustre-node-<pod> -c azurelustre --tail=300 | grep -i "sub-dir"
```

**Resolution:**

- Ensure template variables are properly formatted, for example:

  ```yaml
  sub-dir: "apps/${pod.metadata.namespace}/${pod.metadata.name}"
  ```

- Validate subdirectory path doesn't escape the filesystem root
- Use valid subdirectory paths without `../` patterns

---

#### Error: Subdirectory contains a symlink

`FailedPrecondition` with `sub-dir ... contains symlink component ...` means the
driver found a symlink while preparing a new writable subdirectory mount. Lustre
cannot mount through that path. The driver leaves the link unchanged and does not
create directories through it.

Use a directory-only `sub-dir` path, or coordinate a filesystem correction with
its owner. Do not delete shared entries simply to clear the error. See
[subdirectory creation](driver-parameters.md#subdirectory-creation) for readonly
behavior.

If the error reports `Unavailable` because temporary mount cleanup is incomplete,
keep the driver running so cleanup can be retried.

---

## Configuration Errors

### StorageClass Parameter Errors

#### Error: Cannot unmarshal number

**Symptoms:**

- StorageClass validation fails
- Output of kubectl contains:
  - `Error from server (BadRequest)`
  - `StorageClass in version "v1" cannot be handled as a StorageClass:`
  - `json: cannot unmarshal number into Go struct field StorageClass.parameters of type string`

**Possible Causes:**

- Zone provided as integer number instead of string in yaml configuration

**Debugging Steps:**

```bash
# Check StorageClass zone configuration
grep "zone" <storageclass>.yaml

# You should see:
zone: "1"
# You should not see:
zone: 1
```

**Resolution:**

- Specify zone as a string: `zone: "1"`
- Use zone values that are available for your SKU and location (zones are dynamically validated against Azure SKU capabilities)

---

#### Error: CreateVolume Parameter zone must be provided for dynamically provisioned AMLFS

**Symptoms:**

- Dynamic provisioning fails with missing zone parameter
- Controller logs show: `CreateVolume Parameter zone must be provided for dynamically provisioned AMLFS in location <location>, available zones: [...]`

**Possible Causes:**

- The `zone` parameter is not specified in the StorageClass
- The specified SKU and location combination requires a zone to be specified

**Debugging Steps:**

```bash
# Check StorageClass zone configuration
kubectl get storageclass <storageclass> -o yaml | grep -i "zone\|sku-name\|location"

# Check controller logs for available zones
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep "available zones"
```

**Resolution:**

- Add the `zone` parameter to your StorageClass with a value from the available zones list shown in the error message
- Example: `zone: "1"`
- If available zones are not apparent from the logs, check [Find all skus and available zones for a location](csi-debug.md#find-all-skus-and-available-zones-for-a-location)

---

#### Error: CreateVolume Parameter zone cannot be used in location, no zones available for SKU

**Symptoms:**

- Dynamic provisioning fails with zone parameter specified but no zones available
- Controller logs show: `CreateVolume Parameter zone cannot be used in location <location>, no zones available for SKU <sku>`

**Possible Causes:**

- The location/SKU combination does not have zone support

**Debugging Steps:**

```bash
# Check the SKU and location configuration
kubectl get storageclass <storageclass> -o yaml | grep -i "sku-name\|location"
```

**Resolution:**

- Remove the `zone` parameter from your StorageClass for this SKU/location combination
- Choose a different SKU that supports zones in your location
- Use a different location where the SKU supports zones
- To confirm whether zones are enabled, check [Find all skus and available zones for a location](csi-debug.md#find-all-skus-and-available-zones-for-a-location)

---

#### Error: CreateVolume Parameter zone must be one of available zones

**Symptoms:**

- Dynamic provisioning fails with invalid zone value
- Controller logs show: `CreateVolume Parameter zone <zone> must be one of: [...]`

**Possible Causes:**

- The specified zone is not available for the SKU in the given location
- The zone value doesn't match the zones supported by the Azure SKU

**Debugging Steps:**

```bash
# Check the current zone configuration
kubectl get storageclass <storageclass> -o yaml | grep "zone"

# Check controller logs for available zones
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep "zone .* must be one of"
```

**Resolution:**

- Update the `zone` parameter to use one of the zones listed in the error message
- Example: If error shows `[1, 2]`, use `zone: "1"` or `zone: "2"`
- If available zones are not apparent from the logs, check [Find all skus and available zones for a location](csi-debug.md#find-all-skus-and-available-zones-for-a-location)

---

#### Error: CreateVolume Parameter sku-name must be one of supported values

**Symptoms:**

- Dynamic provisioning fails with invalid SKU error
- Controller logs show: `CreateVolume Parameter sku-name must be one of: [...]`

**Possible Causes:**

- Unsupported or misspelled SKU name in the specified location
- Case sensitivity in SKU name
- SKU not available in the specified Azure region

**Debugging Steps:**

```bash
# Check StorageClass SKU configuration
kubectl get storageclass <storageclass> -o yaml | grep sku-name

# Check controller logs for available SKUs
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep "must be one of"
```

**Resolution:**

- Use one of the SKU values listed in the error message
- Common supported SKU values include:
  - `AMLFS-Durable-Premium-40`
  - `AMLFS-Durable-Premium-125`
  - `AMLFS-Durable-Premium-250`
  - `AMLFS-Durable-Premium-500`
- Verify the SKU is available in your target location

---

#### Warning: SKU retrieval failures

**Symptoms:**

- The driver cannot use the requested SKU's metadata and defers SKU, zone and capacity validation
  to ARM during creation
- Controller logs show one or more of the following:
  - `cannot retrieve SKU metadata for <sku> in location <location>: ...; continuing creation ...`
  - `error retrieving SKUs: ...`
  - `found no AMLFS SKUs for location <location>`
  - `could not find location info for sku <sku> in location <location>`
  - `received incomplete SKU response ...` or `received invalid SKU response ...`

**Possible Causes:**

- Azure API connectivity issues
- Insufficient permissions to list Azure SKUs
- Invalid or unsupported Azure region
- Missing or malformed SKU response fields

**Debugging Steps:**

```bash
# Check controller logs for SKU retrieval warnings
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "sku\|retrieving"

# Verify location parameter in StorageClass
kubectl get storageclass <storageclass> -o yaml | grep location

```

**Resolution:**

- Verify Azure region name is correct (e.g., `eastus`, `westus2`)
- No subscription-wide permission grant is required to proceed. Optional SKU
  discovery permissions enable earlier validation; see
  [optional fail-fast checks](driver-parameters.md#optional-fail-fast-checks).
- Check that the SKU, zone and PVC capacity are supported in the target region.
  Without SKU metadata, capacity uses the driver's 4-TiB fallback increment
  rather than the SKU-specific increment.
- If creation fails, inspect the subsequent ARM creation or subnet-check error.
- Permission denials from subnet sizing or VNet usage are also warnings; other
  subnet errors and known insufficient capacity still stop creation.
- See [SKU discovery and capacity validation](dynamic-provisioning.md#sku-discovery-and-capacity-validation)
  for capacity rounding.

---

#### Error: CreateVolume Parameter maintenance-time-of-day-utc must be in HH\:MM format

**Symptoms:**

- Dynamic provisioning fails with invalid time format error
- Controller log contains time format error specified above

**Debugging Steps:**

```bash
# Check maintenance time format
kubectl get storageclass <storageclass> -o yaml | grep maintenance-time
```

**Resolution:**

- Use 24-hour format: `maintenance-time-of-day-utc: "23:00"`
- Ensure leading zeros for single digits: `"02:00"` not `"2:00"`

---

### Kubernetes resource quota restriction

#### Error: Dynamic provisioning would exceed default quota

**Symptoms:**

- StorageClass validation fails
- Output of kubectl contains:
  - `Error from server (Forbidden)`
  - `exceeded quota: pvc-lustre-dynprov-quota`
  - `requested: dynprov.azurelustre.csi.azure.com.storageclass.storage.k8s.io/persistentvolumeclaims=1`
  - `used: dynprov.azurelustre.csi.azure.com.storageclass.storage.k8s.io/persistentvolumeclaims=1`
  - `limited: dynprov.azurelustre.csi.azure.com.storageclass.storage.k8s.io/persistentvolumeclaims=1`

**Possible Causes:**

- In our example storage class yaml, we include a resource quota to limit the number of dynamically
  created AMLFS clusters for a given StorageClass to 1
- This is to prevent the user from accidentally creating many AMLFS clusters without explicit approval

**Debugging Steps:**

```bash
# Check kubernetes resourcequotas
kubectl describe resourcequotas -A

# Example output:
Name:                                                                                 pvc-lustre-dynprov-quota
Namespace:                                                                            default
Resource                                                                              Used  Hard
--------                                                                              ----  ----
dynprov.azurelustre.csi.azure.com.storageclass.storage.k8s.io/persistentvolumeclaims  1     1

# If you see Used = 1, you have already created a persistent volume claim for this storage class
```

**Resolution:**

- If you want to keep using the example storage class but increase the quota per given storage class:

```bash
# Edit the quota in the storage class yaml file:
apiVersion: v1
kind: ResourceQuota
metadata:
  name: pvc-lustre-dynprov-quota
spec:
  hard:
    dynprov.azurelustre.csi.azure.com.storageclass.storage.k8s.io/persistentvolumeclaims: "1"
    # Change this value to be higher ^

# Alternative approach:
# If you have already created the storage class you can also edit it in place like so:
kubectl edit resourcequotas pvc-lustre-dynprov-quota

# Change the following value to be greater than "1"
...
spec:
  hard:
    dynprov.azurelustre.csi.azure.com.storageclass.storage.k8s.io/persistentvolumeclaims: "1"
...
```

- If you do not want to have a pvc limit for each storage class, you can remove the `ResourceQuota`
  section from the example storage class yaml, or create your own without that section.

---

## Debugging and Troubleshooting

### Log Collection

#### Controller Logs

```bash
# Real-time logs from all controller pods
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre -f --prefix

# Specific time range logs
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --since=1h

# Save logs to file
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=-1 > controller-logs.txt
```

#### Node Logs

```bash
# Real-time logs from all node pods
kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre -f --prefix

# Logs from specific node
kubectl logs  -n kube-system csi-azurelustre-node-<pod-id> -c azurelustre --tail=-1

# Save node logs to file
kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre --tail=-1 > node-logs.txt
```

#### Comprehensive Log Collection

Use the provided log collection script:

```bash
# Download and run the log collection utility
curl -O https://raw.githubusercontent.com/kubernetes-sigs/azurelustre-csi-driver/main/utils/azurelustre_log.sh
chmod +x azurelustre_log.sh
./azurelustre_log.sh > lustre-debug.logs 2>&1
```

For additional support, collect logs using the utility script and consult the [CSI Debug Guide](csi-debug.md) for more detailed troubleshooting steps.
