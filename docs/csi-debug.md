# CSI Driver Troubleshooting Guide

Examples use `kube-system` and static-manifest workload names. For Helm installs,
substitute your release namespace and the workload names returned by:

```sh
kubectl get pods,deployments,daemonsets -A -l app.kubernetes.io/name=azurelustre-csi-driver
```

---

## Driver Readiness and Health Issues

> **Node pod container model (read this first).** Each `csi-azurelustre-node`
> pod runs four containers, and **which container you target with `-c` matters**
> for every command below:
>
> | Container | Role | Probes | Look here for |
> | --------- | ---- | ------ | ------------- |
> | `lustre-loader` | native sidecar that installs the full client, loads missing modules, configures LNet, and reconciles it | `readinessProbe` = `/app/readinessProbe.sh`; `livenessProbe` = `/app/startupState.sh live`; no blocking startup probe | kernel modules, LNet/NIDs, metapackage install |
> | `azurelustre` | serves CSI immediately and installs userspace tools in a retrying worker | `startupProbe` + `livenessProbe` = `/healthz` on port 29763; readiness requires the CSI socket and `/app/readinessProbe.sh` | userspace setup, runtime evidence, mounts, CSI logs |
> | `liveness-probe` | exposes the driver `/healthz` to the kubelet | — | — |
> | `node-driver-registrar` | registers the driver socket with the kubelet | `startupProbe` + `livenessProbe` = `/healthz` (port 29764; registration socket responds) | kubelet registration |
>
> A node pod is `Ready` (`4/4`) only when the `lustre-loader` sidecar reports
> setup complete and LNet healthy, and the driver passes socket-plus-runtime
> readiness. A restarting loader can leave the Pod NotReady while new mounts
> still succeed against fresh, supported host runtime. Socket existence alone
> does not verify a successful CSI RPC. LNet and
> kernel-module troubleshooting targets `-c lustre-loader`; mount and CSI driver
> troubleshooting targets `-c azurelustre`.

### Controller provisioner health

The controller's `csi-provisioner` exposes `/healthz/leader-election` on port
29761, separate from the CSI driver's `/healthz` on port 29762. The provisioner
starts its HTTP server only after connecting to and initializing with the CSI
driver. Its startup probe allows about ten minutes for that initialization,
holding off liveness checks until the endpoint responds successfully.

After startup, the liveness probe can restart a provisioner whose leader-election
health check fails. A successful response does not mean that this replica is the
leader or that a volume operation succeeded; standby replicas can also be healthy.
Check `-c csi-provisioner` logs for election and provisioning errors, and
`-c azurelustre` logs for CSI driver errors.

### LNet readiness troubleshooting (loader sidecar)

**Symptoms:**

- CSI driver node pods show less than `4/4` ready, or stay in `Init:` for a long time
- The `lustre-loader` sidecar's readiness probe fails repeatedly
- Pods requiring Lustre volumes stay `Pending` because the node never becomes Ready
- Mount operations fail with "driver not ready" errors

#### Probe configuration

```sh
kubectl describe -n kube-system pod -l app=csi-azurelustre-node
```

In the `lustre-loader` init container you should see:

- `Readiness: exec [/app/readinessProbe.sh]`
- `Liveness: exec [/app/startupState.sh live]`
- No loader startup probe with the current source defaults

and in the `azurelustre` container:

- `Startup: http-get http://:healthz/healthz`
- `Readiness: exec [/bin/sh -c test -S /csi/csi.sock && /app/readinessProbe.sh]`
- `Liveness: http-get http://:healthz/healthz`

During initial setup, readiness can fail while CSI already serves cleanup.
The loader's liveness check permits ten minutes of setup before restarting a
stalled attempt. The driver's startup probe can fail until its CSI socket
exists, and the registrar's startup probe may fail until its registration socket
responds. The registrar's startup probe holds off liveness checks with a budget of
about one minute. It does not extend the registrar's 30-second CSI connection
timeout: if that expires, the registrar exits and Kubernetes restarts it with
backoff. Its HTTP health check does not confirm completed kubelet registration; check the
registrar logs for `NotifyRegistrationStatus` when diagnosing registration failures.

With the explicit Helm opt-out `node.cleanupCapableStartup=false`, the legacy
loader startup probe still gates main-container startup, and driver userspace
installation blocks CSI. Diagnose that mode separately from cleanup-first startup.

#### Run the LNet readiness probe directly (loader sidecar)

```sh
kubectl exec -n kube-system <pod-name> -c lustre-loader -- /app/readinessProbe.sh
```

Exit code 0 prints `LNet readiness checks passed`. On failure (exit code 1) the
script prints the specific reason — use it to decide where to look next:

| Probe message | Meaning | Where to look next |
| ------------- | ------- | ------------------ |
| `Loader setup is incomplete` | the current loader attempt has not completed | loader package/setup logs |
| `Driver runtime observation is unavailable or stale` | the driver lacks current client/policy-bound health evidence | driver setup logs, loaded-client version, projected compatibility policy |
| `LNet not available or not configured` | `lnetctl net show` failed; LNet is not up | loader logs and kernel-module load (below) |
| `No LNet NIDs configured` | LNet is up but has no NID | LNet tcp net / interface configuration |
| `LNet ping functionality not available` | `lnetctl ping` is missing | userspace tooling in the loader image |
| `Unable to determine LNet NID for self-ping test` | only a loopback NID is present | tcp interface not added to LNet |
| `LNet self-ping test failed for NID: <nid>` | NID present but not pingable | node networking / LNet |
| `No LNet interfaces in 'up' state` | the LNet interface is down | node networking |

#### Loader sidecar logs

```sh
kubectl logs -n kube-system -l app=csi-azurelustre-node -c lustre-loader --tail=50
```

Look for:

- `Loading the LNet.` — modules being loaded on a fresh pod
- `LNet is loaded skip the load` — modules already resident (e.g. after a sidecar restart)
- `Running Lustre kernel module version <v> matches the installed client.` — the loaded modules are the version this pod installed
- `WARNING: the Lustre client upgrade did not take effect on this node` — the resident module differs from the installed client; see [Teardown behavior](#teardown-behavior-sigterm-and-termination)
- `Loader ready; entering LNet reconcile loop` — startup complete
- `LNet tcp network missing interfaces; reconfiguring.` / `Adding interface: eth0` — the reconcile loop repairing config drift
- `LNet reconciliation did not complete this cycle` — a reconcile pass failed; the loop retries, but repeated occurrences mean LNet cannot be repaired in place
- `Termination signal received; exiting without unloading resident Lustre/LNet modules.` — ordinary termination preserves the node's modules, including when idle
- `Error: Could not install necessary Lustre packages` followed by `Note: the package manager cannot distinguish…` — the install failed; the note explains why "package not found" is ambiguous

### Driver container not Ready

**Symptoms:**

- The `lustre-loader` sidecar is Ready, but the pod still shows less than `4/4`
- The `azurelustre` container's socket-plus-runtime readiness probe is failing
- The `azurelustre` container's `RESTARTS` count is climbing, or it is in `CrashLoopBackOff`

The driver opens CSI before installing userspace tools. Setup failures leave
ordinary cleanup available while the worker retries once per minute.
After successful setup, it checks LNet every five seconds and binds success to
the loaded client and projected policy fingerprint. Observations expire after
90 seconds; changed client or policy data invalidates them immediately on the
next readiness/admission check. Unknown or unsupported resident clients deny
new mounts even when optional image admission is off.

Loader readiness alone does not control driver readiness. A loader restart is
compatible with new mounts if driver utilities and supported resident runtime
remain healthy. Optional image admission still requires current container
identity/digest evidence. Existing mounted-target retries and ordinary unmounts
bypass the fresh-mount gate.

`Startup probe failed` events alone do not identify the cause. Liveness checks
are suppressed until the startup probe succeeds, but the startup probe can
itself restart the container when its failure budget is exhausted. Increasing
`RESTARTS` can also result from driver initialization or serving errors,
cleanup timeouts, or OOM kills. Inspect the termination details and logs using
the [driver lifecycle troubleshooting steps](#driver-startup-and-shutdown-failures).

```sh
# Driver startup + install logs; look for "Listening for connections"
kubectl logs -n kube-system <pod-name> -c azurelustre --tail=100

# Logs from the previous attempt if the container is restarting
kubectl logs -n kube-system <pod-name> -c azurelustre --previous --tail=100

# Check socket-file readiness; this alone does not establish gRPC health
kubectl exec -n kube-system <pod-name> -c azurelustre -- test -S /csi/csi.sock

# Confirm the userspace tools installed (these come from the driver's utils install)
kubectl exec -n kube-system <pod-name> -c azurelustre -- sh -c 'command -v mount.lustre lnetctl'

# Check client/policy-bound runtime readiness, not just the CSI socket
kubectl exec -n kube-system <pod-name> -c azurelustre -- /app/readinessProbe.sh
```

If the install failed, the driver logs show the `tdnf`/`apt-get` error followed
by `Error: Could not install necessary Lustre packages`. Common causes: the node
cannot reach the package feed, or the requested Lustre version
(`LUSTRE_VERSION` / `CLIENT_SHA_SUFFIX`) does not exist for the node kernel.

> [!NOTE]
> Kernel modules are **not** installed by the driver container — the
> `lustre-loader` sidecar already loaded them into the shared host kernel. The
> driver container only installs the kernel-agnostic userspace tools.

### Driver startup and shutdown failures

This section applies to the `azurelustre` container in both controller and node
pods. The loader's kernel-module cleanup is a separate
[teardown step](#teardown-behavior-sigterm-and-termination).

For the running gRPC server, a completed SIGTERM/SIGINT shutdown exits
successfully. Startup or serving failures and cleanup timeouts exit with an
error. The [shutdown contract](../README.md#driver-shutdown) describes the
ten-second cleanup deadline and the work that can continue in Azure afterward.
This deadline is separate from both pod termination grace and the provisioning
request timeout.

Capture the affected container's logs and the pod's termination details before
restarting it:

```sh
kubectl describe pod <pod-name> -n <driver-namespace>
kubectl logs <pod-name> -n <driver-namespace> -c azurelustre --timestamps --tail=200

# Use this when a previous instance of the container exists in this pod
kubectl logs <pod-name> -n <driver-namespace> -c azurelustre --previous --timestamps --tail=200
```

In `describe`, inspect the `azurelustre` container's `State`, `Last State`,
termination reason, exit code, and timestamps, along with the pod events.
Correlate these with rollout, drain, probe-failure, and OOM events; a restart
count or socket file alone does not establish the cause. `--previous` retrieves
the prior container instance in the same pod, not a deleted pod. Use cluster
log aggregation for pods that have already been removed.

For controller provisioning failures, also capture the client's view:

```sh
kubectl logs <controller-pod-name> -n <driver-namespace> -c csi-provisioner --timestamps --tail=200
```

Use the [lifecycle error reference](errors.md#driver-lifecycle-errors) to
distinguish initialization failures, rejected or canceled requests, and cleanup
timeouts. Do not treat a ten-second shutdown timeout as the longer-running
[AMLFS creation timeout](errors.md#error-amlfs-cluster-creation-timed-out).
Before an intentional restart, follow the workload and provisioning
precautions in the [shutdown contract](../README.md#driver-shutdown). Neither
successful shutdown nor a cancellation error proves that an Azure operation
has finished; check its resource state before taking further action.

### Loader sidecar restarting (`lustre-loader` CrashLoopBackOff / RESTARTS climbing)

**Symptoms:**

- `kubectl get pod` shows the `lustre-loader` init container with a climbing `RESTARTS` count

The loader's liveness check permits ten minutes of setup. After completed
setup, it checks that the `lnet` kernel module remains loaded. Setup failures
can also exit the loader and trigger Kubernetes restarts. A restart attempts
package recovery and loads missing modules. Transient LNet configuration drift
does not itself trigger liveness failure; the reconcile loop repairs it in place.

```sh
# Why did it restart?
kubectl describe -n kube-system pod <pod-name> | grep -A15 lustre-loader

# Is the module actually present?
kubectl exec -n kube-system <pod-name> -c lustre-loader -- test -d /sys/module/lnet && echo loaded || echo "GONE"

# Loader logs across the restart
kubectl logs -n kube-system <pod-name> -c lustre-loader --previous --tail=50
```

If the module keeps disappearing, check for host-level actions unloading Lustre
modules (e.g. another agent running `lustre_rmmod`, or a node kernel update that
invalidates the loaded modules).

### Manual LNet debugging (loader sidecar)

All LNet and kernel-module inspection runs in the **`lustre-loader`** container,
which is where the modules are loaded and LNet is configured:

```sh
# Check kernel modules
kubectl exec -n kube-system <pod-name> -c lustre-loader -- sh -c 'lsmod | grep -E "lnet|lustre"'

# List LNet NIDs
kubectl exec -n kube-system <pod-name> -c lustre-loader -- lctl list_nids

# Show the LNet tcp network and interface status
kubectl exec -n kube-system <pod-name> -c lustre-loader -- lnetctl net show --net tcp

# Self-ping a NID
kubectl exec -n kube-system <pod-name> -c lustre-loader -- lnetctl ping <nid>
```

### Teardown behavior (SIGTERM and termination)

The built-in entrypoint traps `SIGTERM` and `SIGINT` and exits without unloading
resident Lustre/LNet modules or unconfiguring LNet. This applies during startup
and normal operation, whether the node is busy or idle. A container restart does
not establish that the host's modules are safe to remove.

The replacement loader installs its configured packages and runs `modprobe`,
which leaves already loaded modules in place. If the resident client differs
from the installed client, startup logs both versions. Existing mounts continue
using the resident module. A new pod image, successful package installation, or
zero module reference count does not prove that the desired client is active.
Only explicitly qualified driver/tools/module combinations should be used.

Routine termination is separate from the opt-in experimental completion-gated
retirement worker. That worker still requires a valid request, unloads without
force, and verifies module absence before acknowledging completion. Ordinary
termination does not request or acknowledge retirement. Ordinary package-install
recovery also preserves resident Lustre/LNet modules and their packages, even
when idle or different from the requested client. It removes conflicting
packages only when no Lustre/LNet modules remain. An incompatible resident
client or unresolved package conflict may require the approved node lifecycle
procedure below; an install failure does not authorize module replacement.

There is no steady-state idle activation loop yet. Draining a node and deleting
its CSI pod alone is not a reliable way to activate a different client. Use an
approved node restart/replacement procedure, such as a supported
[AKS node-image upgrade](https://learn.microsoft.com/azure/aks/node-image-upgrade),
and verify the loaded version afterward. Do not force module removal or bypass
workload shutdown, unmount, or disruption protections.

A signal interrupts the loader's idle `wait` promptly. Bash can defer the
handler while a foreground command such as `tdnf` or `apt` is running, so
termination may still reach the pod's grace period. Modules from a previous
container may already be resident during package installation; this is not
evidence of an empty host.

```sh
# Inspect the actual resident client, not just the image or installed package.
kubectl exec -n kube-system <node-pod> -c lustre-loader -- cat /sys/module/lustre/version
kubectl logs -n kube-system <node-pod> -c lustre-loader | grep -E "Running Lustre kernel module|upgrade did not take effect"
```

### Watch probe events in real time

```sh
kubectl get events --field-selector involvedObject.name=<pod-name> -n kube-system -w | grep -E "Readiness|Liveness|Startup"
```

---

## OS-Specific DaemonSet Issues

The Azure Lustre CSI driver uses distribution-specific DaemonSets to ensure proper Lustre client compatibility across different OS versions. Each DaemonSet targets specific node OS versions using node selectors and affinity rules.

### No CSI Driver Pods on Nodes

**Symptoms:**

- Nodes exist in the cluster but have no CSI driver pods running
- Pods requiring Lustre volumes remain in `Pending` or `ContainerCreating` state
- kubectl shows `0/N nodes are available` for pods with Lustre PVCs
- Node has no CSI driver pod scheduled despite DaemonSets being deployed

**Check node OS labels:**

```sh
# Check the OS SKU label on all nodes
kubectl get nodes -o custom-columns=NAME:.metadata.name,OS-SKU:.metadata.labels.'kubernetes\.azure\.com/os-sku-effective'
```

Expected output:

```text
NAME                                OS-SKU
aks-nodepool1-12345678-vmss000000  Ubuntu2204
aks-nodepool2-23456789-vmss000001  Ubuntu2404
aks-nodepool3-34567890-vmss000002  AzureLinux3
```

**Possible Causes:**

- Node OS version doesn't match any DaemonSet selector
- Missing or incorrect `kubernetes.azure.com/os-sku-effective` label
- Unsupported OS version (supported: Ubuntu 22.04, Ubuntu 24.04, Azure Linux 3)
- Node labels were manually modified or are missing

**Debugging Steps:**

```sh
# List all CSI driver pods and their nodes
kubectl get pods -n kube-system -l app=csi-azurelustre-node -o wide

# Check which DaemonSets are deployed
kubectl get daemonsets -n kube-system -l app=csi-azurelustre-node

# Check DaemonSet desired vs ready counts
kubectl get ds -n kube-system csi-azurelustre-node-jammy
kubectl get ds -n kube-system csi-azurelustre-node-noble
kubectl get ds -n kube-system csi-azurelustre-node-azurelinux3

# Inspect node labels in detail
kubectl get node <node-name> --show-labels | grep os-sku-effective

# Check DaemonSet pod scheduling status for a specific node
kubectl get events -n kube-system --field-selector involvedObject.kind=DaemonSet --sort-by='.lastTimestamp' | grep csi-azurelustre-node
```

**Resolution:**

1. **For nodes with missing or non-standard labels:**

   Modify the DaemonSet node selector or affinity rules to match your node's actual labels:

   ```sh
   # Edit the DaemonSet to adjust node selectors
   kubectl edit ds -n kube-system csi-azurelustre-node-jammy

   # Or modify the affinity matchExpressions to include your node's label value
   # For example, add additional values to the kubernetes.azure.com/os-sku-effective operator
   ```

   Example modification for the affinity section:

   ```yaml
   affinity:
     nodeAffinity:
       requiredDuringSchedulingIgnoredDuringExecution:
         nodeSelectorTerms:
           - matchExpressions:
               - key: kubernetes.azure.com/os-sku-effective
                 operator: In
                 values:
                   - Ubuntu2204
                   - Ubuntu2004
                   - AzureLinux3
                   - <your-custom-label-value>
   ```

   After editing, the DaemonSet will automatically schedule pods on matching nodes.

2. **If upgrading from driver version < v0.4.0:**

   For a manifest installation, use the [in-place upgrade
   procedure](./install-csi-driver.md#install-with-kubectl), not uninstall/reinstall.
   Stop workloads and wait for unmounts as described in the [upgrade
   precautions](../charts/README.md#upgrade), but keep existing PVCs and PVs.
   For a Helm installation, use `helm upgrade` instead of the script below.

   This is disruptive: first shut down affected consumers and complete the
   mount/reference inspection above on every affected node. Do not uninstall
   a driver supporting live mounts as a way to bypass staged activation.

   ```sh
   # From a checkout of the intended driver version; preserves PVCs and PVs
   ./deploy/install-driver.sh local
   ```

   Versions prior to v0.4.0 used a single DaemonSet without OS-specific targeting, which conflicts with the new distribution-specific architecture.

3. **For unsupported OS versions:**

   - Upgrade node pool to Ubuntu 22.04, Ubuntu 24.04, or Azure Linux 3
   - Create a new node pool with a supported OS version
   - Migrate workloads to supported nodes

---

### Multiple CSI Driver Pods on Same Node

**Symptoms:**

- A single node has multiple CSI driver pods running simultaneously
- Multiple DaemonSet pods from different flavors (jammy/noble/azurelinux3) on the same node
- Unexpected behavior or conflicts during volume mounting
- Resource contention on nodes

**Check for multiple pods per node:**

```sh
# List all CSI driver pods grouped by node
kubectl get pods -n kube-system -l app=csi-azurelustre-node -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName,FLAVOR:.metadata.labels.flavor | sort -k2

# Count CSI driver pods per node
kubectl get pods -n kube-system -l app=csi-azurelustre-node -o json | \
  jq -r '.items[] | "\(.spec.nodeName) \(.metadata.labels.flavor)"' | \
  sort | uniq -c
```

**Possible Causes:**

- Node has multiple matching OS SKU labels (should not happen in normal AKS)
- DaemonSet node selectors are overlapping or misconfigured
- Manual label modifications creating ambiguous node targeting
- Label `kubernetes.azure.com/os-sku-effective` changed while pods were running

**Debugging Steps:**

```sh
# Check node labels that match multiple DaemonSets
kubectl get node <node-name> -o jsonpath='{.metadata.labels}' | jq

# Inspect DaemonSet node affinity rules
kubectl get ds -n kube-system csi-azurelustre-node-jammy -o yaml | grep -A20 affinity
kubectl get ds -n kube-system csi-azurelustre-node-noble -o yaml | grep -A20 affinity
kubectl get ds -n kube-system csi-azurelustre-node-azurelinux3 -o yaml | grep -A20 affinity

# Check for pods that should not be on a node
kubectl describe pod -n kube-system <duplicate-pod-name> | grep -A10 "Node-Selectors\|Node Affinity"
```

**Resolution:**

1. **Identify the correct OS version:**

   ```sh
   # Check actual OS on the node
   kubectl debug node/<node-name> -it --image=mcr.microsoft.com/cbl-mariner/busybox:2.0 -- cat /host/etc/os-release
   ```

2. **Remove incorrect pods:**

   ```sh
   # Delete the pod that doesn't match the node's actual OS
   kubectl delete pod -n kube-system <incorrect-pod-name>
   ```

3. **Fix node labels if incorrect:**

   ```sh
   # Remove incorrect label
   kubectl label node <node-name> kubernetes.azure.com/os-sku-effective-

   # Add correct label
   kubectl label node <node-name> kubernetes.azure.com/os-sku-effective=Ubuntu2204
   ```

4. **If upgrading from driver version < v0.4.0:**

   For a manifest installation, use the [in-place upgrade
   procedure](./install-csi-driver.md#install-with-kubectl), not uninstall/reinstall.
   Stop workloads and wait for unmounts as described in the [upgrade
   precautions](../charts/README.md#upgrade), but keep existing PVCs and PVs.
   For a Helm installation, use `helm upgrade` instead of the script below.

   ```sh
   # From a checkout of the intended driver version; preserves PVCs and PVs
   ./deploy/install-driver.sh local
   ```

   Versions prior to v0.4.0 used a single DaemonSet without OS-specific targeting, which can result in multiple pods on the same node when upgrading.

5. **Prevent future occurrences:**

   - Avoid manually modifying OS-related node labels
   - Let AKS manage the `kubernetes.azure.com/os-sku-effective` label
   - Review any automation that might be modifying node labels

---

### DaemonSet Pod Image Pull Failures

**Symptoms:**

- CSI driver pods stuck in `ImagePullBackOff` or `ErrImagePull` state
- Events show image pull errors for specific OS-tagged images
- Some nodes have working pods while others fail

**Check pod status and events:**

```sh
# List pods with image pull issues
kubectl get pods -n kube-system -l app=csi-azurelustre-node | grep -E "ImagePullBackOff|ErrImagePull"

# Describe failing pod for detailed error
kubectl describe pod -n kube-system <pod-name>
```

Look for error messages like:

- `Failed to pull image "mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-jammy": rpc error`
- `manifest for mcr.microsoft.com/.../azurelustre-csi:v0.4.0-noble not found`

**Possible Causes:**

- Image tag doesn't exist for the specified OS flavor
- Network connectivity problems to MCR (Microsoft Container Registry)
- Incorrect image tag in DaemonSet specification
- Registry authentication issues (For personal repositories during CSI testing/development)
- Private registry configuration issues (For personal repositories during CSI testing/development)

**Debugging Steps:**

```sh
# Verify image tags in DaemonSets
kubectl get ds -n kube-system csi-azurelustre-node-jammy -o jsonpath='{.spec.template.spec.containers[?(@.name=="azurelustre")].image}'
kubectl get ds -n kube-system csi-azurelustre-node-noble -o jsonpath='{.spec.template.spec.containers[?(@.name=="azurelustre")].image}'
kubectl get ds -n kube-system csi-azurelustre-node-azurelinux3 -o jsonpath='{.spec.template.spec.containers[?(@.name=="azurelustre")].image}'

# Check image pull secrets
kubectl get serviceaccount -n kube-system csi-azurelustre-node-sa -o yaml

# Test image pull manually on a node (requires privileged debugging access)
kubectl debug node/<node-name> -it --image=mcr.microsoft.com/cbl-mariner/busybox:2.0 --profile=sysadmin -- chroot /host /bin/sh
# Then inside the debug pod:
# crictl pull mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-jammy
```

**Resolution:**

1. **Verify image exists:**

   ```sh
   # Check available tags (requires docker or crane)
   docker manifest inspect mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-jammy
   ```

2. **Use correct image tags:**

   - Ensure DaemonSet manifests use existing image tags
   - Verify `-jammy`, `-noble`, and `-azurelinux3` suffixes match available images
   - Update to a known working version if needed

3. **Fix network/registry access:**

   - Verify cluster can access mcr.microsoft.com
   - Check firewall rules and network policies
   - Verify no proxy configuration issues

---

### Inconsistent Driver Versions Across Nodes

**Symptoms:**

- Different CSI driver versions running on different nodes
- Inconsistent behavior across the cluster
- Some volumes mount successfully while others fail with different errors
- Version mismatch warnings in logs

**Check driver versions:**

```sh
# Check image versions across all CSI driver pods
kubectl get pods -n kube-system -l app=csi-azurelustre-node -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.nodeName}{"\t"}{.spec.containers[?(@.name=="azurelustre")].image}{"\n"}{end}' | column -t

# Check DaemonSet specifications
kubectl get ds -n kube-system csi-azurelustre-node-jammy -o jsonpath='{.spec.template.spec.containers[?(@.name=="azurelustre")].image}'
kubectl get ds -n kube-system csi-azurelustre-node-noble -o jsonpath='{.spec.template.spec.containers[?(@.name=="azurelustre")].image}'
kubectl get ds -n kube-system csi-azurelustre-node-azurelinux3 -o jsonpath='{.spec.template.spec.containers[?(@.name=="azurelustre")].image}'
```

**Possible Causes:**

- An explicitly configured `OnDelete` DaemonSet has a newer desired template while existing pods
  intentionally remain on their current revision
- Different image tags configured for jammy, noble, or azurelinux3 DaemonSets
- Failed DaemonSet updates leaving some pods on old versions
- Manual pod restarts using different images

**Debugging Steps:**

```sh
# Show the controller-derived view of every AMLFS-capable node
kubectl get azurelustrenodestatuses -n kube-system -o wide

# Compare desired and current pod state
kubectl get ds -n kube-system -l app=csi-azurelustre-node -o wide

# Review DaemonSet update history
kubectl rollout history ds/csi-azurelustre-node-jammy -n kube-system
kubectl rollout history ds/csi-azurelustre-node-noble -n kube-system
kubectl rollout history ds/csi-azurelustre-node-azurelinux3 -n kube-system

# Check pod ages to identify old pods
kubectl get pods -n kube-system -l app=csi-azurelustre-node -o custom-columns='NAME:.metadata.name,AGE:.metadata.creationTimestamp,IMAGE:.spec.containers[?(@.name=="azurelustre")].image'

# Map nodes to AKS pools before deciding which nodes need lifecycle activation
kubectl get nodes -L kubernetes.azure.com/agentpool
```

`Exact` means the resident client matches current desired state.
`CompatibleNonCurrent` means the resident client is intentionally allowed but
is not current; this can occur during either forward rollout or rollback.
Within each `kubernetes.azure.com/agentpool`, compare the Node, Client, and CSI
columns. A pool is mixed when its nodes do not report the same Client/CSI
combination.

**Resolution:**

The default `RollingUpdate` strategy automatically replaces pods when their
desired template changes. For compatible updates with module-preserving
loaders, troubleshoot stalled rollout or readiness failures rather than
draining every node. Supported `CompatibleNonCurrent` modules can remain loaded
after all CSI pods reach the desired revision.

The manual node procedure below is for explicit staging or transitions that
require maintenance, not routine compatible driver delivery. Follow the
[upgrade precautions](../charts/README.md#upgrade) before activating a staged
revision.

1. **Identify nodes that still run an older pod revision:**

   ```sh
   kubectl get pods -n kube-system -l app=csi-azurelustre-node \
     -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName,IMAGE:.spec.containers[0].image,CREATED:.metadata.creationTimestamp
   ```

2. **For staged or incompatible transitions, update one node at a time:**

   This procedure updates the CSI pod template, not necessarily the resident
   kernel module. If a different client must become active, use a supported
   node lifecycle operation and verify the loaded version. See
   [termination behavior](#teardown-behavior-sigterm-and-termination).

   Plan workload disruption, available capacity, PodDisruptionBudgets, and
   application shutdown with the workload owner first. This is not a
   zero-downtime procedure. Do not blindly switch a pending `OnDelete` DaemonSet
   to `RollingUpdate`: that immediately activates its staged template. Review
   the running loader's termination behavior and the target client compatibility
   first. Incompatible transitions still need drained-node maintenance.
   Pause conflicting deployment/upgrade automation during the operation.

   With enforcement enabled, API access failures or stale controller/node
   evidence block new mounts and post-reboot remounts. Policy edits can also
   briefly deny new mounts until projection and status fingerprints converge.
   Deleting the compatibility-policy ConfigMap does not disable enforcement:
   capable pods fail closed until a valid policy returns. Review the
   [admission rollout and recovery guidance](../charts/README.md#chart-configuration)
   before changing policy during maintenance.

   Set the actual driver namespace (the examples elsewhere use `kube-system`)
   and select the CSI pod on this exact node, not just one matching the pool name.
   Check its controller owner and record its UID/revision before proceeding.

   ```bash
   set -euo pipefail
   NODE='<node-name>'
   CSI_NAMESPACE='kube-system'
   kubectl get pods -n "${CSI_NAMESPACE}" -l app=csi-azurelustre-node \
     --field-selector "spec.nodeName=${NODE}" -o wide
   CSI_POD='<current-node-csi-pod>'
   kubectl get pod -n "${CSI_NAMESPACE}" "${CSI_POD}" -o yaml
   # Record any pre-existing admission annotation; do not clear another owner's denial.
   kubectl get node "${NODE}" -o yaml

   # When admission enforcement is enabled, block new mounts first.
   kubectl annotate node "${NODE}" \
     azurelustre.csi.azure.com/mount-admission=denied --overwrite
   kubectl get azurelustrenodestatuses -n "${CSI_NAMESPACE}" "${NODE}" -o yaml
   ```

   When using admission quiescence, wait for a fresh status reporting
   `mountAdmission: Denied` with `reason: AdministrativeQuiescence` before
   relying on it. The annotation is not an enforcement barrier on an older
   driver or when admission enforcement is disabled. In those cases,
   coordinate stopping all mount creators separately.

   Admission does not tear down an existing mount. However, **remounting after
   a reboot is a new mount and can be denied**, even for a pre-existing pod or
   PVC. Pod replacement, rescheduling, or loss of the prior mount also requires
   new admission. Do not assume existing workloads will recover while admission
   is denied. Preserve shutdown/unmount access while blocking new work.

   ```bash
   kubectl cordon "${NODE}"
   kubectl drain "${NODE}" --ignore-daemonsets

   # Inspect remaining workloads, including DaemonSets and static/mirror pods.
   kubectl get pods -A --field-selector "spec.nodeName=${NODE}" -o json |
     jq '.items[] | {namespace: .metadata.namespace, name: .metadata.name,
       owners: .metadata.ownerReferences, volumes: .spec.volumes}'
   ```

   **Stop** if drain fails, a PDB blocks eviction, a Lustre consumer remains,
   or any inspection fails. Do not add `--force`, bypass eviction protection,
   or use lazy/forced unmount to make this check pass. Drain ignores
   DaemonSets and does not prove kubelet staging mounts or host mounts are gone.
   Trace remaining PVCs to their PVs in the PVC's namespace, and inspect inline
   CSI volumes and host-managed consumers too.

   **Inspect mounts and references before deleting the driver pod.** First
   check the driver's mount namespace; the command prints Lustre mounts and
   returns nonzero if any remain:

   ```bash
   kubectl exec -n "${CSI_NAMESPACE}" "${CSI_POD}" -c azurelustre -- \
     awk '/ - lustre / { print; found=1 } END { exit found }' /proc/self/mountinfo
   ```

   This is only the container's view, even though kubelet mounts are propagated
   into it. `/proc/1` inside a non-hostPID pod is **not** the host's PID 1.
   Using approved privileged **host access on the target node**, run the
   following read-only checks. Do not run these on the operator's workstation:

   ```bash
   sudo bash -ceu '
     declare -A seen=()
     for process in /proc/[0-9]*; do
       namespace=$(readlink "${process}/ns/mnt")
       [[ -n "${seen[${namespace}]:-}" ]] && continue
       seen["${namespace}"]=1
       echo "Checking ${process}/mountinfo (${namespace})"
       awk "/ - lustre / { print; found=1 } END { exit found }" "${process}/mountinfo"
     done
     if [[ -d /sys/module/lustre ]]; then
       references=$(cat /sys/module/lustre/refcnt)
       holders=$(find /sys/module/lustre/holders -mindepth 1 -maxdepth 1 -print)
       printf "lustre refcnt=%s holders=%s\n" "${references}" "${holders}"
       [[ "${references}" == 0 && -z "${holders}" ]]
     fi
   '
   sudo lsns --type mnt --output NS,NPROCS,PID,COMMAND
   ```

   **Stop** on any Lustre mount, nonzero reference count, module holder,
   permission/read error, or unexplained namespace. A process exiting during
   inspection can fail the scan; repeat only once the node is quiescent, not by
   ignoring the failure. Process-visible namespaces are not exhaustive:
   persistent namespaces, open files, detached mounts, and bind-mount references
   need host/runtime investigation. Empty container output alone is never
   clearance. If the host/reference checks are unavailable or inconclusive,
   keep the node cordoned and escalate; do not unload modules to "test" safety.

   Only after these checks pass, replace the already-drained node's CSI pod:

   ```bash
   kubectl delete pod -n "${CSI_NAMESPACE}" "${CSI_POD}" --wait=true
   kubectl get pods -n "${CSI_NAMESPACE}" -l app=csi-azurelustre-node \
     --field-selector "spec.nodeName=${NODE}" -o wide
   REPLACEMENT_POD='<replacement-node-csi-pod>'
   kubectl wait -n "${CSI_NAMESPACE}" --for=condition=Ready \
     "pod/${REPLACEMENT_POD}" --timeout=10m
   ```

   Do not delete or restart an AMLFS node pod while Lustre workloads remain on
   its node. Repeat this sequence for each old node in the mixed pool; do not
   drain the entire pool at once. Prefer the supported AKS node lifecycle
   operation for the pool (for example, a NodeImage upgrade) when it is
   available, and use direct pod deletion only for an already drained node.

3. **Verify each updated node before releasing quiescence or scheduling:**

   ```bash
   kubectl get node "${NODE}" -o yaml
   kubectl get pod -n "${CSI_NAMESPACE}" "${REPLACEMENT_POD}" -o yaml
   kubectl get azurelustrenodestatuses -n "${CSI_NAMESPACE}" "${NODE}" -o yaml
   ```

   Keep the node cordoned. Verify exactly one non-terminating Ready CSI pod
   on this node, a new pod UID, the intended image and pod revision, and
   current reporter evidence tied to that pod/Node UID. Confirm a recent
   `observedAt`, `reporterFreshness: Current`, and acceptable
   `clientCompatibility`, `csiDelivery`, and `securityCompliance`.
   Compare `desired.flavor`, `desired.driverImageDigest`, and
   `desired.loaderImageDigest` with this pod's flavor and observed image IDs.
   Driver and loader approval lists are both per-flavor: approving a Noble
   digest does not authorize it on Jammy. `NonCurrentApproved` means an image
   is approved for this flavor but is not its desired digest; this is separate
   from `csiDelivery`, which compares DaemonSet revisions. See
   [per-flavor image approval](../charts/README.md#per-flavor-image-approval).
   Also inspect `observed.factRenewTime`: a recent controller write alone
   is not a fresh node heartbeat. New-mount admission requires a heartbeat
   no older than 90 seconds, matching node/pod/container identity and local
   boot/kernel/loaded-client facts. `desired.policyFingerprint` binds the
   decision to the exact projected policy; a policy change denies new mounts
   until a matching decision is available. ConfigMap projection is eventually
   consistent, so policy changes are not instantaneous across nodes.
   While quiesced, admission should still be `Denied` for
   `AdministrativeQuiescence`; that reason alone is not health evidence.
   Missing/stale/unknown evidence, an unexpected revision, or any other denial
   is a **stop condition**. Keep quiescence and cordon in place and investigate
   the status `reason`/`message` and reporter/loader logs.

   If many nodes show aging controller status, inspect the dedicated
   status-controller Deployment, its `/healthz` and `/readyz` probes on port
   `29654`, and the election Lease in the namespace named by its
   `STATUS_CONTROLLER_ELECTION_NAMESPACE` environment variable. Both replicas
   should be Ready even though only one reconciles. Ready does not guarantee
   fresh per-node status. Renew failures terminate the leader; a stuck
   reconciliation fails its probes after the progress watchdog expires.
   Check logs for API throttling, status-write conflicts, and sixty-second
   sweep timeouts before considering capacity changes. See
   [controller availability and permissions](../charts/README.md#status-controller-availability-and-permissions)
   for the lock-isolation migration and ownership rules.

   After reviewing the replacement evidence, remove **only an annotation
   introduced by this maintenance operation** (do not erase a pre-existing
   administrative denial), then re-read status while still cordoned:

   ```bash
   kubectl annotate node "${NODE}" azurelustre.csi.azure.com/mount-admission-
   kubectl get azurelustrenodestatuses -n "${CSI_NAMESPACE}" "${NODE}" -o yaml
   ```

   Wait for a new observation reflecting annotation removal and confirming
   `mountAdmission: Allowed`, with current reporter/compatibility/security
   evidence. If denial or stale evidence persists, restore the maintenance
   denial, leave the node cordoned, and investigate. Do not turn enforcement
   off or broaden an allowlist just to complete maintenance.

   Only after this admission check, and workload-owner approval, uncordon:

   ```bash
   kubectl uncordon "${NODE}"
   ```

   Validate new mount and application I/O recovery before updating another
   node. Readiness alone does not prove mount admission or workload recovery.

4. **Align image versions:**

   Ensure all three DaemonSets use the same base version (only differing by OS suffix):
   - Jammy: `v0.7.0-jammy`
   - Noble: `v0.7.0-noble`
   - Azure Linux 3: `v0.7.0-azurelinux3`

   All three should share the same version number (`v0.7.0` in this example).

5. **If upgrading from driver version < v0.4.0:**

   For a manifest installation, use the [in-place upgrade
   procedure](./install-csi-driver.md#install-with-kubectl), not uninstall/reinstall.
   Stop workloads and wait for unmounts as described in the [upgrade
   precautions](../charts/README.md#upgrade), but keep existing PVCs and PVs.
   For a Helm installation, use `helm upgrade` instead of the script below.

   ```sh
   # From a checkout of the intended driver version; preserves PVCs and PVs
   ./deploy/install-driver.sh local
   ```

   Versions prior to v0.4.0 used a single DaemonSet which can cause version inconsistencies when mixing with the new OS-specific DaemonSets.

6. **Verify update strategy:**

   ```sh
   # Check the node-pod update strategy
   kubectl get ds -n kube-system csi-azurelustre-node-jammy -o jsonpath='{.spec.updateStrategy}'
   ```

   The default is:

   ```yaml
   type: RollingUpdate
   rollingUpdate:
     maxUnavailable: 10%
   ```

   The budget applies independently to each flavor DaemonSet. Explicit
   `OnDelete` remains available to stage a template without replacing existing
   pods; its strategy has no `rollingUpdate` section.

**Long-haul lifecycle validation prerequisites:**

[`test/long-haul/update-test.sh`](../test/long-haul/update-test.sh) is a disruptive
test for an approved test pool, not a production convergence command. Before
running it, every active node DaemonSet must already be observed, fully
converged to its current template, and have Ready/available pods, with exactly
one CSI pod per pool node. For the migration case, arrange the original
`RollingUpdate` baseline on an empty test pool **before mounting workloads**;
the script also accepts a converged `OnDelete` baseline. A pending `OnDelete`
baseline requires a separate approved drained-node lifecycle, not a strategy
flip or bulk pod deletion. Pause competing chart updates and pool scaling.

The test matches controller-owned `ControllerRevisions` to the actual desired
pod template after the DaemonSet observes its generation, then pins that hash.
It verifies staging retains pod UIDs/readiness and lifecycle activation has
new pod UIDs, the pinned revision, and changed node UID or boot ID. An AKS
upgrade that does not actually recycle the nodes cannot supply this evidence
and fails the test. The synthetic template annotation proves staged-template
activation, not a client-version change or zero downtime; application recovery
and real client compatibility require their own validation.

---

### DaemonSet Not Scheduling on New Nodes

**Symptoms:**

- Newly added nodes don't get CSI driver pods
- Cluster scaling or node pool additions don't automatically deploy drivers
- DaemonSet desired count doesn't increase with new nodes

**Check DaemonSet coverage:**

```sh
# Compare node count vs DaemonSet pod count
echo "Total nodes: $(kubectl get nodes --no-headers | wc -l)"
echo "Jammy pods: $(kubectl get pods -n kube-system -l app=csi-azurelustre-node,flavor=jammy --no-headers | wc -l)"
echo "Noble pods: $(kubectl get pods -n kube-system -l app=csi-azurelustre-node,flavor=noble --no-headers | wc -l)"
echo "AzureLinux3 pods: $(kubectl get pods -n kube-system -l app=csi-azurelustre-node,flavor=azurelinux3 --no-headers | wc -l)"

# List nodes without CSI driver pods
comm -23 \
  <(kubectl get nodes -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | sort) \
  <(kubectl get pods -n kube-system -l app=csi-azurelustre-node -o jsonpath='{.items[*].spec.nodeName}' | tr ' ' '\n' | sort)

# Check which nodes match DaemonSet selectors
kubectl get nodes -l kubernetes.io/os=linux -o custom-columns=NAME:.metadata.name,OS-SKU:.metadata.labels.'kubernetes\.azure\.com/os-sku-effective'
```

**Possible Causes:**

- New nodes have taints that DaemonSet doesn't tolerate
- Node labels don't match any DaemonSet selector (wrong OS version)
- DaemonSet update or creation failed
- Resource constraints preventing pod scheduling

**Debugging Steps:**

```sh
# Check node taints
kubectl describe node <new-node-name> | grep -A5 Taints

# Check DaemonSet tolerations
kubectl get ds -n kube-system csi-azurelustre-node-jammy -o jsonpath='{.spec.template.spec.tolerations}'

# Check for scheduling events
kubectl get events -n kube-system --sort-by='.lastTimestamp' | grep -E "csi-azurelustre-node|<new-node-name>"

# Verify DaemonSet controller is working
kubectl logs -n kube-system -l component=kube-controller-manager | grep -i daemonset
```

**Resolution:**

1. **Verify node labels:**

   ```sh
   # Ensure new node has required labels
   kubectl label node <new-node-name> kubernetes.azure.com/os-sku-effective=Ubuntu2204
   ```

2. **Check DaemonSet health:**

   ```sh
   # Verify DaemonSets are active
   kubectl get ds -n kube-system -l app=csi-azurelustre-node

   # If DaemonSet is missing or corrupted, redeploy
   kubectl apply -f deploy/csi-azurelustre-node-jammy.yaml
   kubectl apply -f deploy/csi-azurelustre-node-noble.yaml
   kubectl apply -f deploy/csi-azurelustre-node-azurelinux3.yaml
   ```

3. **Force pod creation if needed:**

   ```sh
   # Delete and recreate DaemonSet (last resort)
   kubectl delete ds -n kube-system csi-azurelustre-node-jammy
   kubectl apply -f deploy/csi-azurelustre-node-jammy.yaml
   ```

---

## Volume Provisioning Issues

### Dynamic Provisioning (AMLFS Cluster Creation)

**Symptoms:**

- PVC remains in `Pending` status for an extended period (more than 15–20 minutes)
- Dynamic provisioning StorageClass is configured, but the AMLFS cluster is not created
- PVC events show provisioning errors or timeouts

**Check PVC status and events:**

```sh
kubectl describe pvc <pvc-name>
```

Look for events such as:

- `waiting for a volume to be created`
- `failed to provision volume`
- `error creating AMLFS cluster`

Use the [dynamic provisioning error reference](errors.md#dynamic-provisioning-errors)
to interpret the reported error.

**Check controller logs for dynamic provisioning errors:**

```sh
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "dynamic\|provision\|amlfs\|create"
```

Common error patterns:

- Authentication/authorization errors
- Quota exceeded errors
- Network/subnet configuration issues
- Invalid StorageClass parameters

Consult [Troubleshoot Azure Managed Lustre deployment issues](https://learn.microsoft.com/en-us/azure/azure-managed-lustre/troubleshoot-deployment)

**Verify StorageClass configuration:**

```sh
kubectl get storageclass <storageclass-name> -o yaml
```

Check for:

- Correct provisioner: `azurelustre.csi.azure.com`
- Valid SKU name, zone (if required), and maintenance window parameters
- Proper network configuration (`vnet-name`, `subnet-name`, etc.)
- Resource group and location settings
- Zone parameter matches available zones for the SKU and location

**Check Azure subscription quotas and limits:**

```sh
# Check if you have reached the AMLFS cluster limit in your subscription
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "quota\|limit\|insufficient"
```

**Verify Azure permissions for the controller's identity:**

Check the [required permissions](driver-parameters.md#permissions-for-kubelet-identity)
on the kubelet identity, or on the federated identity when workload identity is
enabled. Missing optional discovery permissions do not prevent provisioning.

Check for permission errors in the controller logs:

```sh
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "forbidden\|unauthorized\|permission"
```

**Monitor AMLFS cluster creation progress in the Azure portal:**

1. Navigate to Azure portal → Resource Groups
2. Look for the resource group specified in the StorageClass (or the AKS infrastructure RG if not specified)
3. Check if the AMLFS cluster resource is being created
   - The AMLFS cluster will have tags corresponding to the volume it was created for, example below:
     - k8s-azure-created-by: kubernetes-azurelustre-csi-driver
     - kubernetes.io-created-for-pv-name: pvc-78876f95-32c2-41c4-bdfa-eb92d1eeb341
     - kubernetes.io-created-for-pvc-name: pvc-lustre-dynprov
     - kubernetes.io-created-for-pvc-namespace: default
4. Review the Activity Log for the failed create/update event. If PVC events or
   logs show only `Failed` or a generic health message, follow
   [AMLFS provisioning failed](errors.md#error-amlfs-provisioning-failed)
   to find the service error, including after the filesystem has been recreated.

**Check for network issues:**

```sh
# Verify that the specified virtual network and subnet exist and are accessible
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "network\|subnet\|vnet"
```

Common network issues:

- The virtual network or subnet does not exist
- Insufficient IP addresses in the subnet
- Network security group blocking traffic
- Missing virtual network peering
- Missing outbound connectivity from the AMLFS filesystem subnet

**Check for zone configuration issues:**

```sh
# Verify zone parameter and available zones
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "zone\|available zones"

# Check StorageClass zone configuration
kubectl get storageclass <storageclass-name> -o yaml | grep -E "zone"
```

Common zone issues:

- Zone parameter not specified when required for the SKU/location
- Zone value not available for the specified SKU in the location
- Zone specified when the SKU doesn't support zones in the location

See [What are availability zones?](https://learn.microsoft.com/en-us/azure/reliability/availability-zones-overview?tabs=azure-cli#regions-that-support-availability-zones)
for more information about availability zones.

#### Find all skus and available zones for a location

These manual queries use your Azure CLI identity and require
[SKU lookup permission](driver-parameters.md#optional-fail-fast-checks).

```sh
# Find all sku values and available zones for a location:
# Fill SUBSCRIPTION and LOCATION parameters with your values:
# NOTE: Requires bash or other modern shell. If using 'sh', you can just run the 'az' command through 'grep'
#   though the output may lose the headers or include other locations depending on your grep pattern.
LOCATION="<your-location>" ; \
SUBSCRIPTION="<your-subscription-id>" ; \
location_data=$(az rest --method get --uri "/subscriptions/${SUBSCRIPTION}/providers/Microsoft.StorageCache/skus?api-version=2024-03-01" --query "value[?contains(name, 'AMLFS')].{location: locationInfo[0].location, sku: name, zones: join(', ', sort(locationInfo[0].zones)), zoneDetails: join(', ', locationInfo[].zoneDetails[])} | sort_by(@, &sku) | sort_by(@, &length(sku)) | sort_by(@, &location)" -o table | uniq) && head -n 2 <<< ${location_data} && grep -i -E "^${LOCATION}\b" <<< ${location_data}
```

For subscriptions / locations with zones available, you should see something like the following:

```text
Location    Sku                        Zones    ZoneDetails
----------  -------------------------  -------  -------------
eastus      AMLFS-Durable-Premium-40   1, 2, 3
eastus      AMLFS-Durable-Premium-125  1, 2, 3
eastus      AMLFS-Durable-Premium-250  1, 2, 3
eastus      AMLFS-Durable-Premium-500  1, 2, 3
```

For subscriptions / locations without zones enabled, you'll see something like the following:

```text
Location    Sku                        Zones    ZoneDetails
----------  -------------------------  -------  -------------
westus      AMLFS-Durable-Premium-40
westus      AMLFS-Durable-Premium-125
westus      AMLFS-Durable-Premium-250
westus      AMLFS-Durable-Premium-500
```

#### Find skus and available zones for all locations

```sh
# Find available zones for all locations:
# Fill SUBSCRIPTION parameter with your value:
SUBSCRIPTION="<your-subscription-id>" ; \
az rest --method get --uri "/subscriptions/${SUBSCRIPTION}/providers/Microsoft.StorageCache/skus?api-version=2024-03-01" --query "value[?contains(name, 'AMLFS')].{location: locationInfo[0].location, sku: name, zones: join(', ', sort(locationInfo[0].zones)), zoneDetails: join(', ', locationInfo[].zoneDetails[])} | sort_by(@, &sku) | sort_by(@, &length(sku)) | sort_by(@, &location)" -o table | uniq
```

---

#### SKU lookup and validation

```sh
# Check for SKU-related errors in controller logs
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "sku\|retrieving"
```

Common SKU-related issues:

- SKU name not supported in the specified location
- Azure API errors when retrieving available SKUs
- Insufficient permissions to list Azure SKUs
- Invalid or misspelled SKU names

If SKU discovery is unavailable, the driver warns and continues with the
configured SKU and zone, rounding capacity in 4-TiB increments. Azure validates
the creation request. Diagnose any subsequent creation or subnet-check error,
rather than treating the discovery warning as the failure. See
[SKU retrieval warnings](errors.md#warning-sku-retrieval-failures).

**Verify SKU availability for a location:**

```sh
# Verify StorageClass SKU and location configuration
kubectl get storageclass <storageclass-name> -o yaml | grep -E "sku-name|location"

# Check controller logs for specific SKU validation errors
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep "must be one of"
```

Check [Find all skus and available zones for a location](#find-all-skus-and-available-zones-for-a-location). If the location is not supported for AMLFS, the output will be empty.

### Workload Identity (Dynamic Provisioning)

**Symptoms:**

- Dynamic provisioning fails with authentication errors only on clusters that have workload identity enabled
- Controller logs show token-exchange failures such as `AADSTS70021` or `AADSTS70025`
- The client ID reported in ARM authorization errors is the node (kubelet) identity, not the identity you federated

**Confirm what the controller is using:**

```sh
# The controller service account should carry the client-id annotation when WI is configured
kubectl get serviceaccount csi-azurelustre-controller-sa -n kube-system -o yaml | grep -i "azure.workload.identity"

# The controller pods should have the injected workload-identity env vars
POD=$(kubectl get pod -n kube-system -l app=csi-azurelustre-controller -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n kube-system "$POD" -c azurelustre -- env | grep AZURE_
```

Expect `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, and `AZURE_FEDERATED_TOKEN_FILE` to all be present. If `AZURE_CLIENT_ID` is missing while the other two are set, the ServiceAccount is missing its `azure.workload.identity/client-id` annotation.

Confirm which identity the controller authenticates with:

```sh
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "authenticating with"
# expect: authenticating with workload identity (client ID "<client-id>")
```

`authenticating with managed identity` means workload identity was not enabled on the chart. With it enabled, `AZURE_TOKEN_CREDENTIALS=WorkloadIdentityCredential` restricts the controller to that one credential with no managed-identity fallback, so if the webhook did not inject `AZURE_FEDERATED_TOKEN_FILE` the log instead reads `configured for workload identity but AZURE_FEDERATED_TOKEN_FILE is not set` and Azure calls fail until that is fixed.

Only the controller authenticates to Azure. Node pods never call ARM, so they build no credential and log no identity line.

**Common causes:**

- `IsWorkloadIdentityEnabled` was not set to `Enabled`, so no annotation or label is rendered. Reinstall or upgrade with `--set IsWorkloadIdentityEnabled=Enabled --set IdentityClientId=<client-id>` (see [Workload Identity](workload-identity.md)).
- The federated credential subject does not match the controller ServiceAccount. It must be `system:serviceaccount:<namespace>:csi-azurelustre-controller-sa`.
- The identity referenced by `IdentityClientId` lacks the required AMLFS permissions. It needs the same permissions listed under [Permissions For Kubelet Identity](driver-parameters.md#permissions-for-kubelet-identity).

Check for solutions in [Resolving Common Errors](errors.md#authentication-and-authorization-errors)

---

### Static Provisioning (Pre-existing Volumes)

**Symptoms:**

- PVC does not reach `Bound` status
- User workload pod does not reach `Running` status

**Locate the CSI driver pod:**

```sh
kubectl get po -o wide -n kube-system -l app=csi-azurelustre-controller
```

```text
NAME                                              READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azurelustre-controller-56bfddd689-dh5tk       3/3     Running   0          35s     10.240.0.19    k8s-agentpool-22533604-0
csi-azurelustre-controller-56bfddd689-sl4ll       3/3     Running   0          35s     10.240.0.23    k8s-agentpool-22533604-1
```

**Get CSI driver logs:**

```sh
kubectl logs csi-azurelustre-controller-56bfddd689-dh5tk -c azurelustre -n kube-system > csi-lustre-controller.log
```

> **Note:**
>
> - Add `--previous` to retrieve logs from a previously running container.
> - There may be multiple controller pods; logs can be collected from all of them simultaneously:
>
>   ```sh
>   kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=-1 --prefix
>   ```
>
> - To retrieve logs in real time (follow mode):
>
>   ```sh
>   kubectl logs deploy/csi-azurelustre-controller -c azurelustre -f -n kube-system
>   ```

Check for solutions in [Resolving Common Errors](errors.md)

---

## Volume Mount/Unmount Issues

**Locate the CSI driver pod and identify the pod performing the actual volume mount/unmount operation:**

```sh
kubectl get po -o wide -n kube-system -l app=csi-azurelustre-node
```

```text
NAME                                READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azurelustre-node-jammy-9ds7f    4/4     Running   0          7m4s    10.240.0.35    k8s-agentpool-22533604-1
csi-azurelustre-node-jammy-dr4s4    4/4     Running   0          7m4s    10.240.0.4     k8s-agentpool-22533604-0
```

> **Note:** Mounts are performed by the `azurelustre` driver container, so mount
> logs and `mount` output come from `-c azurelustre`. LNet/kernel-module issues
> live in the `lustre-loader` sidecar — see [Driver Readiness and Health Issues](#driver-readiness-and-health-issues).

**Get CSI driver logs:**

```sh
kubectl logs csi-azurelustre-node-jammy-9ds7f -c azurelustre -n kube-system > csi-azurelustre-node.log
```

> **Note:** To watch driver logs in real time across all node DaemonSet pods
> (jammy/noble/azurelinux3) simultaneously, use the label selector:
>
> ```sh
> kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre -f --prefix
> ```

**Check Lustre mounts inside the driver:**

```sh
kubectl exec -it csi-azurelustre-node-jammy-9ds7f -n kube-system -c azurelustre -- mount | grep lustre
```

```text
172.18.8.12@tcp:/lustrefs on /var/lib/kubelet/pods/6632349a-05fd-466f-bc8a-8946617089ce/volumes/kubernetes.io~csi/pvc-841498d9-fa63-418c-8cc7-d94ec27f2ee2/mount type lustre (rw,flock,lazystatfs,encrypt)
172.18.8.12@tcp:/lustrefs on /var/lib/kubelet/pods/6632349a-05fd-466f-bc8a-8946617089ce/volumes/kubernetes.io~csi/pvc-841498d9-fa63-418c-8cc7-d94ec27f2ee2/mount type lustre (rw,flock,lazystatfs,encrypt)
```

> **Note:** It is expected for each mount point to be listed twice

**Reachability is checked before mounting.** Before mounting, the driver checks reachability in two stages: a fast TCP dial to the LNet acceptor port (default `988`), then an `lnetctl ping`. A wrong or unreachable MGS IP fails the dial within about 5 seconds; if a port is open but the peer is not a healthy LNet endpoint, the `lnetctl ping` stage can take up to about 50 seconds before failing. If the MGS is unreachable, `NodePublishVolume` fails fast with a `FailedPrecondition` error (`MGS IP address ... is not reachable`) instead of letting the mount hang. Reproduce the same `lnetctl ping` the driver performs:

```sh
kubectl exec -it csi-azurelustre-node-jammy-9ds7f -n kube-system -c azurelustre -- lnetctl ping <mgs-ip>@tcp
```

> **Note:** The ping result is cached for about 20 seconds, so once connectivity is restored the mount will recover on retry.

Check for solutions in [Resolving Common Errors](errors.md)

---

## Pod Scheduling and Node Readiness Issues

### Pods Stuck in Pending Status with Taint-Related Errors

**Symptoms:**

- Pods requiring Azure Lustre storage remain in `Pending` status
- Pod events show taint-related scheduling failures
- Error messages mentioning `azurelustre.csi.azure.com/agent-not-ready` taint

**Check pod scheduling status:**

```sh
kubectl describe pod <pod-name>
```

Look for events such as:

- `Warning  FailedScheduling  ... node(s) had taint {azurelustre.csi.azure.com/agent-not-ready: }, that the pod didn't tolerate`
- `0/X nodes are available: X node(s) had taint {azurelustre.csi.azure.com/agent-not-ready}`

**Check node taints:**

```sh
kubectl describe nodes | grep -A5 -B5 "azurelustre.csi.azure.com/agent-not-ready"
```

**Check CSI driver readiness on nodes:**

```sh
# Check if CSI driver pods are running on all nodes
kubectl get pods -n kube-system -l app=csi-azurelustre-node -o wide

# Check CSI driver logs for startup issues
kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre --tail=100 | grep -i "taint\|ready\|error"
```

**Common causes and solutions:**

1. **CSI Driver Still Starting**: Wait for CSI driver pods to reach `Running` status

   ```sh
   kubectl wait --for=condition=ready pod -l app=csi-azurelustre-node -n kube-system --timeout=300s
   ```

2. **Lustre Module Loading Issues**: Check if Lustre kernel modules are properly loaded

   ```sh
   kubectl exec -n kube-system <csi-azurelustre-node-pod> -c lustre-loader -- lsmod | grep lustre
   ```

3. **Manual Taint Removal** (Emergency only - not recommended for production):

   ```sh
   kubectl taint nodes <node-name> azurelustre.csi.azure.com/agent-not-ready:NoSchedule-
   ```

**Verify taint removal functionality:**

Check that startup taint removal is enabled in the CSI driver:

```sh
kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre | grep -i "remove.*taint"
```

Expected log output should show taint removal activity when the driver becomes ready.

---

## Get Azure Lustre Driver Version

```sh
kubectl exec -it csi-azurelustre-node-9ds7f -n kube-system -c azurelustre -- /bin/bash -c "./azurelustreplugin --version"
```

```text
Build Date: "2025-07-29T16:54:45Z"
Compiler: gc
Driver Name: azurelustre.csi.azure.com
Driver Version: v1.0.0
Git Commit: 6e8debb72b19181dcff82c81d0fa7fbd949f9337
Go Version: go1.23.10
Platform: linux/amd64
```

---

## Collect Logs for the Lustre CSI Driver Product Team

**Get the utility from `/utils/azurelustre_log.sh`, run it, and share the output `lustre.logs` file:**

```sh
chmod +x ./azurelustre_log.sh
./azurelustre_log.sh > lustre.logs 2>&1
```

---

## Quickly Update Driver Deployment

**Update controller deployment:**

```sh
kubectl edit deployment csi-azurelustre-controller -n kube-system
```

**Update DaemonSet deployment:**

```sh
# Node DaemonSets are per-OS-flavor; edit the one for the affected node OS
kubectl edit ds csi-azurelustre-node-jammy -n kube-system
kubectl edit ds csi-azurelustre-node-noble -n kube-system
kubectl edit ds csi-azurelustre-node-azurelinux3 -n kube-system
```

### Verification Commands

#### Check CSI Driver Status

```bash
# Verify driver pods are running
kubectl get pods -n kube-system -l app=csi-azurelustre-controller
kubectl get pods -n kube-system -l app=csi-azurelustre-node
```

#### Check Volume and Mount Status

```bash
# Check PVC status
kubectl describe pvc <pvc-name>
kubectl get pvc <pvc-name> -o yaml

# Check PV details
kubectl describe pv <pv-name>
kubectl get pv <pv-name> -o yaml

# Check active mounts on nodes
kubectl exec -it -n kube-system csi-azurelustre-node-<pod> -c azurelustre -- mount | grep lustre
```

#### Check Azure Resources

```bash
# List AMLFS clusters in resource group
az amlfs list --resource-group <rg-name>

# Check number of available IP addresses needed for AMLFS cluster
az amlfs get-subnets-size  --sku AMLFS-Durable-Premium-40 --storage-capacity 48
# Example output:
{
  "filesystemSubnetSize": 10
}

# Check subnet IP availability
az amlfs check-amlfs-subnet  --sku AMLFS-Durable-Premium-40 --storage-capacity 48 --location <location> --filesystem-subnet <subnet-id>
# This command will only return with a successful or unsuccessful error code, without output
```

### Other Possible Resolution Steps

1. **Restart the controller or safely replace a node pod**

   Diagnose the failure first. Before an intentional restart, follow the
   [shutdown precautions](../README.md#driver-shutdown): stop new provisioning
   and let in-flight volume operations finish. For node pods, stop the affected
   workloads and confirm their volumes are unmounted before restarting the
   driver; see [teardown behavior](#teardown-behavior-sigterm-and-termination).

   ```bash
   kubectl rollout restart -n kube-system deployment/csi-azurelustre-controller
   ```

   For node pods, follow the complete
   [one-node activation procedure](#inconsistent-driver-versions-across-nodes),
   including drain, host mount/reference inspection, replacement evidence,
   and admission verification **before** uncordoning. A controller restart
   does not activate a staged node client. Do not restart all node pods or
   bypass these gates to troubleshoot a mount failure.

2. **PVC Recreation Is Destructive**

   Do not delete and recreate a PVC to address a canceled request, temporary
   driver unavailability, or a shutdown cleanup timeout. With a `Delete`
   reclaim policy, PVC deletion can also delete the PV, Azure filesystem,
   and its data. If deletion is intentional, verify ownership and data
   retention requirements and follow the
   [safe teardown procedure](../charts/README.md#safe-teardown-procedure) and
   [volume deletion guidance](dynamic-provisioning.md#delete-the-volume).

3. **Check Kubernetes Resource Quotas**

   ```bash
   kubectl describe quota -A
   kubectl describe limitrange -A
   ```

4. **Validate Configuration**

   ```bash
   kubectl get storageclass <storageclass> -o yaml
   kubectl get pv <pv-name> -o yaml
   kubectl get pvc <pvc-name> -o yaml
   ```

5. **Repair the Driver Installation**

   Do not uninstall merely to restart the driver; use an appropriate rollout
   restart or in-place upgrade to preserve existing PVCs and PVs. Follow the
   [upgrade precautions](../charts/README.md#upgrade), including stopping consumers
   and waiting for unmounts before replacing node plugins. For Helm, use
   `helm upgrade`. For a manifest installation, use the installer from a checkout
   of the intended driver version:

   ```bash
   ./deploy/install-driver.sh local
   ```

   If the goal really is driver removal rather than repair, follow the
   [safe teardown procedure](../charts/README.md#safe-teardown-procedure) instead.
   Unmounting alone does not satisfy the guard while matching PVs remain.
