# CSI Driver Troubleshooting Guide

---

## Driver Readiness and Health Issues

> **Node pod container model (read this first).** Each `csi-azurelustre-node`
> pod runs four containers, and **which container you target with `-c` matters**
> for every command below:
>
> | Container | Role | Probes | Look here for |
> | --------- | ---- | ------ | ------------- |
> | `lustre-loader` | native sidecar (init container with `restartPolicy: Always`) that loads the Lustre kernel modules + brings up LNet, then runs an LNet reconcile loop for the life of the pod | `startupProbe` + `readinessProbe` = `/app/readinessProbe.sh` (LNet health); `livenessProbe` = `test -d /sys/module/lnet` | kernel modules, LNet/NIDs, metapackage install |
> | `azurelustre` | CSI driver: installs userspace tools, then serves the gRPC socket | `startupProbe` + `livenessProbe` = `/healthz` (port 29763); `readinessProbe` = `test -S /csi/csi.sock` | userspace utils install, mounts, gRPC/CSI logs |
> | `liveness-probe` | exposes the driver `/healthz` to the kubelet | — | — |
> | `node-driver-registrar` | registers the driver socket with the kubelet | registration `livenessProbe` | kubelet registration |
>
> A node pod is `Ready` (`4/4`) only when the `lustre-loader` sidecar reports
> LNet healthy **and** the `azurelustre` driver socket is serving. LNet and
> kernel-module troubleshooting targets `-c lustre-loader`; mount and CSI driver
> troubleshooting targets `-c azurelustre`.

### LNet readiness troubleshooting (loader sidecar)

**Symptoms:**

- CSI driver node pods show less than `4/4` ready, or stay in `Init:` for a long time
- The `lustre-loader` sidecar's readiness/startup probe fails repeatedly
- Pods requiring Lustre volumes stay `Pending` because the node never becomes Ready
- Mount operations fail with "driver not ready" errors

#### Probe configuration

```sh
kubectl describe -n kube-system pod -l app=csi-azurelustre-node
```

In the `lustre-loader` init container you should see:

- `Startup: exec [/app/readinessProbe.sh]`
- `Readiness: exec [/app/readinessProbe.sh]`
- `Liveness: exec [/bin/sh -c test -d /sys/module/lnet]`

and in the `azurelustre` container:

- `Startup: http-get http://:healthz/healthz`
- `Readiness: exec [/bin/sh -c test -S /csi/csi.sock]`
- `Liveness: http-get http://:healthz/healthz`

During initial startup it is normal to see the loader's startup probe fail a few
times while LNet comes up; once LNet is operational the probes succeed and the
gated `azurelustre` and `node-driver-registrar` containers start. The driver's
startup probe may likewise fail with `connection refused` until its CSI socket
exists. Neither is a problem on a pod that goes on to reach Ready.

#### Run the LNet readiness probe directly (loader sidecar)

```sh
kubectl exec -n kube-system <pod-name> -c lustre-loader -- /app/readinessProbe.sh
```

Exit code 0 prints `LNet readiness checks passed`. On failure (exit code 1) the
script prints the specific reason — use it to decide where to look next:

| Probe message | Meaning | Where to look next |
| ------------- | ------- | ------------------ |
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
- `WARNING: the Lustre client upgrade did not take effect on this node` — the resident modules are an older version and could not be replaced; see [Teardown behavior](#teardown-behavior-sigterm-and-termination)
- `Loader ready; entering LNet reconcile loop` — startup complete
- `LNet tcp network missing interfaces; reconfiguring.` / `Adding interface: eth0` — the reconcile loop repairing config drift
- `LNet reconciliation did not complete this cycle` — a reconcile pass failed; the loop retries, but repeated occurrences mean LNet cannot be repaired in place
- `WARNING: Lustre kernel modules could not be unloaded and remain loaded on this node` — logged by a *terminating* pod; see [Teardown behavior](#teardown-behavior-sigterm-and-termination)
- `Error: Could not install necessary Lustre packages` followed by `Note: the package manager cannot distinguish…` — the install failed; the note explains why "package not found" is ambiguous

### Driver container not Ready (socket not serving)

**Symptoms:**

- The `lustre-loader` sidecar is Ready, but the pod still shows less than `4/4`
- The `azurelustre` container's readiness probe (`test -S /csi/csi.sock`) is failing
- The `azurelustre` container's `RESTARTS` count is climbing, or it is in `CrashLoopBackOff`

The driver container installs its userspace Lustre tools and only then opens the
CSI socket, so it is briefly NotReady on startup (longer on Azure Linux 3, where
the client install is larger). If the install cannot be completed the container
exits rather than opening the socket, so the kubelet restarts it and the node is
never advertised as able to serve mounts it would fail.

`Startup probe failed` events alone indicate only that the install has not yet
completed: the liveness check is suppressed until the startup probe succeeds, so
the container is not restarted while the install is in progress. An increasing
`RESTARTS` count indicates the container is being terminated and recreated,
which means the install is failing or exceeding the startup probe's budget.

```sh
# Driver startup + install logs; look for "Listening for connections"
kubectl logs -n kube-system <pod-name> -c azurelustre --tail=100

# Logs from the previous attempt if the container is restarting
kubectl logs -n kube-system <pod-name> -c azurelustre --previous --tail=100

# Confirm the socket exists (Ready) or not (NotReady)
kubectl exec -n kube-system <pod-name> -c azurelustre -- test -S /csi/csi.sock && echo serving || echo "not serving"

# Confirm the userspace tools installed (these come from the driver's utils install)
kubectl exec -n kube-system <pod-name> -c azurelustre -- sh -c 'command -v mount.lustre lnetctl'
```

If the install failed, the driver logs show the `tdnf`/`apt-get` error followed
by `Error: Could not install necessary Lustre packages`. Common causes: the node
cannot reach the package feed, or the requested Lustre version
(`LUSTRE_VERSION` / `CLIENT_SHA_SUFFIX`) does not exist for the node kernel.

> [!NOTE]
> Kernel modules are **not** installed by the driver container — the
> `lustre-loader` sidecar already loaded them into the shared host kernel. The
> driver container only installs the kernel-agnostic userspace tools.

### Loader sidecar restarting (`lustre-loader` CrashLoopBackOff / RESTARTS climbing)

**Symptoms:**

- `kubectl get pod` shows the `lustre-loader` init container with a climbing `RESTARTS` count

The loader's `livenessProbe` (`test -d /sys/module/lnet`) restarts the sidecar
**only** when the `lnet` kernel module is gone — an unrecoverable in-process
state. The restart reloads the modules. Transient LNet *config* drift does
**not** restart the sidecar; the reconcile loop repairs it in place.

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

On pod deletion the `lustre-loader` sidecar's main loop traps `SIGTERM` and
unloads the Lustre kernel modules so no stale modules are left on the host
(important before a node driver upgrade), then exits within a few seconds rather
than waiting out the full termination grace period. If the modules cannot be
unloaded -- most often because a filesystem is still mounted -- the loader logs
a line beginning with `WARNING:` stating that they remain loaded. (The warning is
visible via cluster log aggregation; a terminating pod's logs are not retained by
`kubectl` after deletion.)

The modules stay resident until something unloads them. There are two ways to
clear them:

1. Reinstall the driver, remove your workloads from the node so the driver can
   unload the modules cleanly, then upgrade or uninstall again. Wait until the
   volumes are actually unmounted before restarting the node pod — deleting a
   workload pod returns before the kubelet has finished unmounting, and the
   container that performs the unmount is the one you are about to restart.
   Confirm with `kubectl exec -n kube-system <node-pod> -c azurelustre -- sh -c 'mount | grep -c lustre'`
   (expect `0`).
2. Reboot the node.

If the modules were left loaded and the driver is later upgraded to a different
Lustre client version, the new loader cannot replace them. It logs a `WARNING:`
on start saying the upgrade did not take effect and naming both versions; mounts
on that node keep using the old client. The same two options above apply.

A pod deleted *during* the loader's initial package install (before the modules
are loaded) exits with nothing to unload, but termination may take up to the
termination grace period (default 30s): the shell only runs its exit handler
once the in-flight `tdnf`/`apt` install command returns. This affects only the
brief install window and leaves no state behind (no modules are loaded yet).

```sh
# Confirm a freshly recreated pod reloaded modules (i.e. the previous pod
# unloaded them on teardown): its loader logs "Loading the LNet." rather than
# "LNet is loaded skip the load".
kubectl logs -n kube-system <new-pod-name> -c lustre-loader | grep -E "Loading the LNet|skip the load"
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

   Uninstall the old driver before deploying the new OS-specific DaemonSets:

   ```sh
   # Uninstall the previous driver version
   ./deploy/uninstall-driver.sh

   # Then install the new version with OS-specific DaemonSets
   ./deploy/install-driver.sh
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
   kubectl debug node/<node-name> -it --image=ubuntu -- cat /etc/os-release
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

   Uninstall the old driver before deploying the new OS-specific DaemonSets:

   ```sh
   # Uninstall the previous driver version
   ./deploy/uninstall-driver.sh

   # Then install the new version with OS-specific DaemonSets
   ./deploy/install-driver.sh
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

# Test image pull manually on a node
kubectl debug node/<node-name> -it --image=ubuntu -- bash
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

- DaemonSet update in progress (rolling update)
- Different image tags configured for jammy, noble, or azurelinux3 DaemonSets
- Failed DaemonSet updates leaving some pods on old versions
- Manual pod restarts using different images

**Debugging Steps:**

```sh
# Check DaemonSet rollout status
kubectl rollout status ds/csi-azurelustre-node-jammy -n kube-system
kubectl rollout status ds/csi-azurelustre-node-noble -n kube-system
kubectl rollout status ds/csi-azurelustre-node-azurelinux3 -n kube-system

# Check for stuck rollouts
kubectl get ds -n kube-system -l app=csi-azurelustre-node -o wide

# Review DaemonSet update history
kubectl rollout history ds/csi-azurelustre-node-jammy -n kube-system
kubectl rollout history ds/csi-azurelustre-node-noble -n kube-system
kubectl rollout history ds/csi-azurelustre-node-azurelinux3 -n kube-system

# Check pod ages to identify old pods
kubectl get pods -n kube-system -l app=csi-azurelustre-node -o custom-columns=NAME:.metadata.name,AGE:.metadata.creationTimestamp,IMAGE:.spec.containers[0].image
```

**Resolution:**

1. **Complete ongoing rollout:**

   ```sh
   # Wait for rollout to complete
   kubectl rollout status ds/csi-azurelustre-node-jammy -n kube-system --timeout=10m
   kubectl rollout status ds/csi-azurelustre-node-noble -n kube-system --timeout=10m
   kubectl rollout status ds/csi-azurelustre-node-azurelinux3 -n kube-system --timeout=10m
   ```

2. **Force pod recreation if stuck:**

   ```sh
   # Delete stuck pods to trigger recreation with new image
   kubectl delete pod -n kube-system <stuck-pod-name>
   ```

3. **Align image versions:**

   Ensure all three DaemonSets use the same base version (only differing by OS suffix):
   - Jammy: `v0.5.0-jammy`
   - Noble: `v0.5.0-noble`
   - Azure Linux 3: `v0.5.0-azurelinux3`

   All three should share the same version number (`v0.5.0` in this example).

4. **If upgrading from driver version < v0.4.0:**

   Uninstall the old driver before deploying the new OS-specific DaemonSets:

   ```sh
   # Uninstall the previous driver version
   ./deploy/uninstall-driver.sh

   # Then install the new version with OS-specific DaemonSets
   ./deploy/install-driver.sh
   ```

   Versions prior to v0.4.0 used a single DaemonSet which can cause version inconsistencies when mixing with the new OS-specific DaemonSets.

5. **Verify update strategy:**

   ```sh
   # Check maxUnavailable setting
   kubectl get ds -n kube-system csi-azurelustre-node-jammy -o jsonpath='{.spec.updateStrategy}'
   ```

   Should be:

   ```yaml
   rollingUpdate:
     maxUnavailable: 10%
   type: RollingUpdate
   ```

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

Check for solutions in [Resolving Common Errors](errors.md)

Consult [Troubleshoot Azure Managed Lustre deployment issues](https://learn.microsoft.com/en-us/azure/azure-managed-lustre/troubleshoot-deployment)

**Check controller logs for dynamic provisioning errors:**

```sh
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "dynamic\|provision\|amlfs\|create"
```

Common error patterns:

- Authentication/authorization errors
- Quota exceeded errors
- Network/subnet configuration issues
- Invalid StorageClass parameters

Check for solutions in [Resolving Common Errors](errors.md)

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

Check for solutions in [Resolving Common Errors](errors.md)

**Check Azure subscription quotas and limits:**

```sh
# Check if you have reached the AMLFS cluster limit in your subscription
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "quota\|limit\|insufficient"
```

Check for solutions in [Resolving Common Errors](errors.md)

**Verify Azure permissions for the kubelet identity:**

Confirm that the driver has the necessary [Permissions For Kubelet Identity](driver-parameters.md#permissions-for-kubelet-identity).

Check for permission errors in the controller logs:

```sh
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "forbidden\|unauthorized\|permission"
```

Check for solutions in [Resolving Common Errors](errors.md)

**Monitor AMLFS cluster creation progress in the Azure portal:**

1. Navigate to Azure portal → Resource Groups
2. Look for the resource group specified in the StorageClass (or the AKS infrastructure RG if not specified)
3. Check if the AMLFS cluster resource is being created
   - The AMLFS cluster will have tags corresponding to the volume it was created for, example below:
     - k8s-azure-created-by: kubernetes-azurelustre-csi-driver
     - kubernetes.io-created-for-pv-name: pvc-78876f95-32c2-41c4-bdfa-eb92d1eeb341
     - kubernetes.io-created-for-pvc-name: pvc-lustre-dynprov
     - kubernetes.io-created-for-pvc-namespace: default
4. Review the Activity Log for any deployment failures

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

Check for solutions in [Resolving Common Errors](errors.md)

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

Check for solutions in [Resolving Common Errors](errors.md)

See [What are availability zones?](https://learn.microsoft.com/en-us/azure/reliability/availability-zones-overview?tabs=azure-cli#regions-that-support-availability-zones)
for more information about availability zones.

#### Find all skus and available zones for a location

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

**Check for SKU retrieval issues:**

```sh
# Check for SKU-related errors in controller logs
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "sku\|retrieving"
```

Common SKU-related issues:

- SKU name not supported in the specified location
- Azure API errors when retrieving available SKUs
- Insufficient permissions to list Azure SKUs
- Invalid or misspelled SKU names

Check for solutions in [Resolving Common Errors](errors.md)

Check [Find all skus and available zones for a location](#find-all-skus-and-available-zones-for-a-location)

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

1. **Restart CSI Driver Pods**

   ```bash
   kubectl rollout restart -n kube-system deployment/csi-azurelustre-controller
   kubectl rollout restart -n kube-system daemonset/csi-azurelustre-node-jammy
   kubectl rollout restart -n kube-system daemonset/csi-azurelustre-node-noble
   kubectl rollout restart -n kube-system daemonset/csi-azurelustre-node-azurelinux3
   ```

2. **Force PVC Recreation**

   ```bash
   kubectl delete pvc <pvc-name>
   kubectl apply -f <pvc-file>.yaml
   ```

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

5. **Reinstall Driver**
    Ensure that all of your volumes are unmounted before uninstalling the driver.

   ```bash
   ./deploy/uninstall-driver.sh
   ./deploy/install-driver.sh
   # You can install other versions by checking them out locally and running a local install
   # See the output of ./deploy/install-driver.sh --help for more information
   ```
