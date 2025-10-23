# Azure Lustre CSI Driver - Resolving Common Errors

This document describes common errors that can occur during volume creation and mounting with the Azure Lustre CSI driver, along with debugging and troubleshooting steps.

## Table of Contents

- [Volume Creation Errors](#volume-creation-errors)
  - [Dynamic Provisioning Errors](#dynamic-provisioning-errors)
    - [Authentication and Authorization Errors](#authentication-and-authorization-errors)
    - [Error: AMLFS cluster creation timed out](#error-amlfs-cluster-creation-timed-out)
    - [Error: Resource not found](#error-resource-not-found)
    - [Error: Cannot create AMLFS cluster, not enough IP addresses available](#error-cannot-create-amlfs-cluster-not-enough-ip-addresses-available)
    - [Error: Reached Azure Subscription Quota Limit for AMLFS Clusters](#error-reached-azure-subscription-quota-limit-for-amlfs-clusters)
- [Pod Scheduling Errors](#pod-scheduling-errors)
  - [Node Readiness and Taint Errors](#node-readiness-and-taint-errors)
    - [Error: Node had taint azurelustre.csi.azure.com/agent-not-ready](#error-node-had-taint-azurelustrecsiazurecomagent-not-ready)
- [Volume Mounting Errors](#volume-mounting-errors)
  - [Node Mount Errors](#node-mount-errors)
    - [Error: Could not mount target](#error-could-not-mount-target)
    - [Error: Context sub-dir must be strict subpath](#error-context-sub-dir-must-be-strict-subpath)
- [Configuration Errors](#configuration-errors)
  - [StorageClass Parameter Errors](#storageclass-parameter-errors)
    - [Error: Cannot unmarshal number](#error-cannot-unmarshal-number)
    - [Error: CreateVolume Parameter zone must be provided for dynamically provisioned AMLFS](#error-createvolume-parameter-zone-must-be-provided-for-dynamically-provisioned-amlfs)
    - [Error: CreateVolume Parameter zone cannot be used in location, no zones available for SKU](#error-createvolume-parameter-zone-cannot-be-used-in-location-no-zones-available-for-sku)
    - [Error: CreateVolume Parameter zone must be one of available zones](#error-createvolume-parameter-zone-must-be-one-of-available-zones)
    - [Error: CreateVolume Parameter sku-name must be one of supported values](#error-createvolume-parameter-sku-name-must-be-one-of-supported-values)
    - [Error: SKU retrieval failures](#error-sku-retrieval-failures)
    - [Error: CreateVolume Parameter maintenance-time-of-day-utc must be in HH:MM format](#error-createvolume-parameter-maintenance-time-of-day-utc-must-be-in-hhmm-format)
  - [Kubernetes resource quota restriction](#kubernetes-resource-quota-restriction)
    - [Error: Dynamic provisioning would exceed default quota](#error-dynamic-provisioning-would-exceed-default-quota)
- [Debugging and Troubleshooting](#debugging-and-troubleshooting)
  - [Log Collection](#log-collection)
    - [Controller Logs](#controller-logs)
    - [Node Logs](#node-logs)
    - [Comprehensive Log Collection](#comprehensive-log-collection)

---

## Volume Creation Errors

### Dynamic Provisioning Errors

> **Note**: Dynamic provisioning functionality is currently in public preview. Some features may not be supported or may have constrained capabilities.

#### Authentication and Authorization Errors

**Symptoms:**

- Controller logs show 403 `Forbidden` or 401 `Unauthorized` HTTP errors
- AMLFS cluster creation fails with permission errors

**Possible Causes:**

- Insufficient Azure RBAC permissions for kubelet identity
- Incorrect identity configuration

**Debugging Steps:**

```bash
# Check controller logs for auth errors
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "forbidden\|unauthorized\|authorization"

# Verify kubelet identity permissions in Azure portal
# Check: IAM (Access control) → Role assignments
```

**Resolution:**

- Assign required RBAC roles to kubelet identity:
  - See [Permissions For Kubelet Identity](driver-parameters.md#Permissions%20For%20Kubelet%20Identity)

---

#### Error: AMLFS cluster creation timed out

**Symptoms:**

- PVC remains in `Pending` status for extended period (>20 minutes)
- Controller logs show messages such as:
  - `AMLFS cluster myapp-lustre-cluster creation timed out. Deleted failed cluster, retrying cluster creation`
  - `AMLFS cluster myapp-lustre-cluster creation did not complete correctly, waiting for deletion to complete before retrying cluster creation`

**Possible Causes:**

- Temporary Azure service issues
- Network connectivity problems during cluster creation
- Insufficient resources in the specified region/zone

**Debugging Steps:**

```bash
# Check controller logs for timeout details
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i 'creation timed out'

# This error should be resolved by the automatic cluster creation retry, if given time to complete. If this issue repeats
# multiple times, or other errors are present, try the following steps:

# Verify Azure service health in your region
# Check Azure portal Service Health dashboard
# Check Azure portal for failed AMLFS cluster resources
# Navigate to: Resource Groups → [your-rg] → Look for failed deployments

# Check for quota limitations
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i quota

# Check controller logs for detailed error messages
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "amlfs\|creation"
```

**Resolution:**

- Wait for automatic cleanup and retry
- Manually delete any stuck AMLFS resources in Azure portal
- Verify network and permissions configuration
- Wait for Azure service issues to resolve if applicable
- Try creating the volume in a different zone or region
- Contact Azure support if timeouts persist

---

#### Error: Resource not found

**Symptoms:**

- Controller logs show one or more of the following:
  - `Resource group 'myapp-rg/myapp-vnet-rg' could not be found.`
  - `The Resource '.../myapp-vnet' under resource group 'myapp-rg' was not found.`
  - `subnet ../myapp-subnet not found in vnet myapp-vnet, resource group myapp-rg. Ensure permissions are correct for configuration`
- Error code: `FailedPrecondition`

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
- Check Azure RBAC permissions for the kubelet identity

---

#### Error: Cannot create AMLFS cluster, not enough IP addresses available

**Symptoms:**

- Controller logs show: `cannot create AMLFS cluster myapp-lustre-cluster in subnet myapp-subnet, not enough IP addresses available`
- Error code: `ResourceExhausted`

**Possible Causes:**

- The specified subnet has insufficient available IP addresses
- AMLFS clusters require a contiguous block of IP addresses

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
- Lustre kernel modules are not yet loaded
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

# Check CSI driver startup logs
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
   # Verify Lustre modules are loaded on nodes
   kubectl exec -n kube-system <csi-azurelustre-node-pod> -c azurelustre -- lsmod | grep lustre
   ```

3. **Verify CSI Driver Configuration**:

   ```bash
   # Check if taint removal is enabled (default: true)
   kubectl get deployment csi-azurelustre-node -n kube-system -o yaml | grep "remove-not-ready-taint"
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
- If available zones are not apparent from the logs, check [csi-debug.md#Find_all_available_zones_for_a_location](Find all available zones for a location)

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
- To confirm whether zones are enabled, check [csi-debug.md#Find_all_available_zones_for_a_location](Find all available zones for a location)

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
- If available zones are not apparent from the logs, check [Find all skus and available zones for a location](csi-debug.md#Find_all_skus_and_available_zones_for_a_location)

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

#### Error: SKU retrieval failures

**Symptoms:**

- Dynamic provisioning fails during SKU validation
- Controller logs show one or more of the following:
  - `error retrieving SKUs: ...`
  - `found no AMLFS SKUs for location <location>`
  - `could not find location info for sku <sku> in location <location>`

**Possible Causes:**

- Azure API connectivity issues
- Insufficient permissions to list Azure SKUs
- Invalid or unsupported Azure region
- SKU not available in the specified location

**Debugging Steps:**

```bash
# Check controller logs for SKU retrieval errors
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 | grep -i "sku\|retrieving"

# Verify location parameter in StorageClass
kubectl get storageclass <storageclass> -o yaml | grep location

# Check Azure permissions for kubelet identity
# Ensure the identity has the correct permissions for the subscription

```

**Resolution:**

- Verify Azure region name is correct (e.g., `eastus`, `westus2`)
- Ensure kubelet identity has sufficient permissions to list Azure SKUs
  - See [Permissions For Kubelet Identity](driver-parameters.md#Permissions%20For%20Kubelet%20Identity)

- Check if AMLFS service is available in your target region
- Retry the operation to handle temporary Azure API issues
- Contact Azure support if SKUs are consistently unavailable in supported regions

---

#### Error: CreateVolume Parameter maintenance-time-of-day-utc must be in HH:MM format

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
