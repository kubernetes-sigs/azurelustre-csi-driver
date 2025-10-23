# CSI Driver Troubleshooting Guide

---

## Driver Readiness and Health Issues

### Enhanced LNet Validation Troubleshooting

**Symptoms:**

- CSI driver node pods show `2/3` ready status
- Readiness probe failing repeatedly
- Pods remain in `Running` or startup issues
- Mount operations fail with "driver not ready" errors

#### Detailed Probe Verification Steps

If the exec-based readiness probe fails (exit code 1), use these detailed verification steps:

```shell
# Verify detailed probe configuration
kubectl describe -n kube-system pod -l app=csi-azurelustre-node
```
Look for exec-based probe configuration in the pod description:
- `Readiness: exec [/app/readinessProbe.sh]`
- `Startup: exec [/app/readinessProbe.sh]`

In the Events section, you may see initial startup probe failures during LNet initialization:
- `Warning Unhealthy ... Startup probe failed: Node pod detected - performing Lustre-specific readiness checks`

This is normal during the initialization phase. Once LNet is fully operational, the probes will succeed.

```shell
# Test the readiness probe script directly
kubectl exec -n kube-system <pod-name> -c azurelustre -- /app/readinessProbe.sh
```
Expected output when working correctly:
- `"Node pod detected - performing Lustre-specific readiness checks"`
- `"All Lustre readiness checks passed"`

```shell
# Check for enhanced validation messages
kubectl logs -n kube-system -l app=csi-azurelustre-node -c azurelustre --tail=20
```
Look for CSI driver startup and readiness messages:
- `"vendor_version":"v0.4.0-readiness-http"` - Confirms feature branch deployment
- Standard CSI GRPC operation logs indicating successful driver initialization

```shell
# Check for detailed validation failure reasons
kubectl logs -n kube-system <pod-name> -c azurelustre | grep -E "(LNet validation failed|Failed to|not operational)"
```

Common issues and solutions:
- **"No valid NIDs"**: LNet networking not properly configured
- **"Self-ping test failed"**: Network connectivity issues
- **"Interfaces not operational"**: Network interfaces not in UP state
- **"Lustre module not loaded"**: Kernel module loading issues

**Test readiness probe directly:**

```sh
# Test the exec-based readiness probe script
kubectl exec -n kube-system <csi-azurelustre-node-pod> -c azurelustre -- /app/readinessProbe.sh
```

Expected responses:
- Exit code 0: Enhanced LNet validation passed
- Exit code 1: One or more validation checks failed (with descriptive error message)

**Test HTTP health endpoints (optional manual testing):**

```sh
# Test enhanced readiness/liveness via HTTP endpoint
kubectl exec -n kube-system <csi-azurelustre-node-pod> -c azurelustre -- curl -s localhost:29763/healthz
```

HTTP responses:
- `/healthz`: `ok` (HTTP 200) or `not ready` (HTTP 503)

**Check enhanced validation logs:**

```sh
# Look for detailed LNet validation messages
kubectl logs -n kube-system <csi-azurelustre-node-pod> -c azurelustre | grep -E "(LNet validation|NIDs|self-ping|interfaces)"
```

Look for validation success messages:
- `"LNet validation passed: all checks successful"`
- `"Found NIDs: <network-identifiers>"`
- `"LNet self-ping to <nid> successful"`
- `"All LNet interfaces operational"`

**Common readiness failure patterns:**

1. **No valid NIDs found:**
   ```text
   LNet validation failed: no valid NIDs
   No valid non-loopback LNet NIDs found
   ```
   **Solution:** Check LNet configuration and network setup

2. **Self-ping test failed:**
   ```text
   LNet validation failed: self-ping test failed
   LNet self-ping to <nid> failed
   ```
   **Solution:** Verify network connectivity and LNet networking

3. **Interfaces not operational:**
   ```text
   LNet validation failed: interfaces not operational
   Found non-operational interface: status: down
   ```
   **Solution:** Check network interface status and configuration

4. **Module loading issues:**
   ```text
   Lustre module not loaded
   LNet kernel module is not loaded
   ```
   **Solution:** Check kernel module installation and loading

**Debug LNet configuration manually:**

```sh
# Check kernel modules
kubectl exec -n kube-system <csi-azurelustre-node-pod> -c azurelustre -- lsmod | grep -E "(lnet|lustre)"

# Check LNet NIDs
kubectl exec -n kube-system <csi-azurelustre-node-pod> -c azurelustre -- lctl list_nids

# Test LNet self-ping
kubectl exec -n kube-system <csi-azurelustre-node-pod> -c azurelustre -- lctl ping <nid>

# Check interface status
kubectl exec -n kube-system <csi-azurelustre-node-pod> -c azurelustre -- lnetctl net show --net tcp
```

**Check probe configuration:**

```sh
# Verify probe settings in deployment
kubectl describe -n kube-system pod <csi-azurelustre-node-pod> | grep -A 10 -E "(Liveness|Readiness|Startup)"
```

**Monitor readiness probe attempts:**

```sh
# Watch probe events in real-time
kubectl get events --field-selector involvedObject.name=<csi-azurelustre-node-pod> -n kube-system -w | grep -E "(Readiness|Liveness)"
```

---

## Volume Provisioning Issues

### Dynamic Provisioning (AMLFS Cluster Creation) - Public Preview

> **Note**: Dynamic provisioning functionality is currently in public preview. Some features may not be supported or may have constrained capabilities.

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

Confirm that the driver has the necessary [Permissions For Kubelet Identity](driver-parameters.md#Permissions%20For%20Kubleet%20Identity).

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

***Find all skus and available zones for a location***

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

***Find skus and available zones for all locations***

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

Check [Find all skus and available zones for a location](csi-debug.md#Find_all_skus_and_available_zones_for_a_location)

**Verify SKU availability for a location:**

```sh
# Verify StorageClass SKU and location configuration
kubectl get storageclass <storageclass-name> -o yaml | grep -E "sku-name|location"

# Check controller logs for specific SKU validation errors
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep "must be one of"
```

Check [Find all skus and available zones for a location](csi-debug.md#Find_all_skus_and_available_zones_for_a_location). If the location is not supported for AMLFS, the output will be empty.

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
NAME                           READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azurelustre-node-9ds7f     3/3     Running   0          7m4s    10.240.0.35    k8s-agentpool-22533604-1
csi-azurelustre-node-dr4s4     3/3     Running   0          7m4s    10.240.0.4     k8s-agentpool-22533604-0
```

**Get CSI driver logs:**

```sh
kubectl logs csi-azurelustre-node-9ds7f -c azurelustre -n kube-system > csi-azurelustre-node.log
```

> **Note:** To watch logs in real time from multiple `csi-azurelustre-node` DaemonSet pods simultaneously, run:
>
> ```sh
> kubectl logs daemonset/csi-azurelustre-node -c azurelustre -n kube-system -f
> ```

**Check Lustre mounts inside the driver:**

```sh
kubectl exec -it csi-azurelustre-node-9ds7f -n kube-system -c azurelustre -- mount | grep lustre
```

```text
172.18.8.12@tcp:/lustrefs on /var/lib/kubelet/pods/6632349a-05fd-466f-bc8a-8946617089ce/volumes/kubernetes.io~csi/pvc-841498d9-fa63-418c-8cc7-d94ec27f2ee2/mount type lustre (rw,flock,lazystatfs,encrypt)
172.18.8.12@tcp:/lustrefs on /var/lib/kubelet/pods/6632349a-05fd-466f-bc8a-8946617089ce/volumes/kubernetes.io~csi/pvc-841498d9-fa63-418c-8cc7-d94ec27f2ee2/mount type lustre (rw,flock,lazystatfs,encrypt)
```

> **Note:** It is expected for each mount mount to be listed twice

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
   kubectl exec -n kube-system <csi-azurelustre-node-pod> -c azurelustre -- lsmod | grep lustre
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
kubectl edit ds csi-azurelustre-node -n kube-system
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
   kubectl rollout restart -n kube-system daemonset/csi-azurelustre-node
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
