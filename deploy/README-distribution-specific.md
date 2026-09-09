# Distribution-Specific Azure Lustre CSI Node Deployments

## Overview

This directory contains distribution-specific DaemonSet deployments for the Azure Lustre CSI driver. Each deployment targets a specific OS version to ensure proper Lustre client compatibility.

## Files

- `csi-azurelustre-node-jammy.yaml` - Ubuntu 22.04 (Jammy) nodes
- `csi-azurelustre-node-noble.yaml` - Ubuntu 24.04 (Noble) nodes
- `csi-azurelustre-node-azurelinux3.yaml` - Azure Linux 3 nodes

## Distribution Targeting

Each deployment uses:

1. **Node Targeting**: Uses node affinity and selectors to match correct node OS flavors
2. **Container Image**: Version-specific image tags like `v0.5.0-jammy`, `v0.5.0-noble`, `v0.5.0-azurelinux3`
3. **Unique Names**: Each DaemonSet has a unique name (`csi-azurelustre-node-jammy`) to prevent conflicts

## Installation

The `install-driver.sh` script deploys all necessary components:

```bash
./install-driver.sh
```

This will:

- Deploy the controller (distribution-agnostic) and the OS-specific node DaemonSets
- Each DaemonSet will only start pods on nodes with matching OS versions

## Node Pool Requirements

Your AKS cluster nodes must have the `kubernetes.azure.com/os-sku-effective` label set to one of:

- `Ubuntu2204`
- `Ubuntu2404`
- `AzureLinux3`

AKS automatically sets this label based on the node pool's OS configuration.

## Image Tags

Container images follow the pattern:

- `mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.5.0-jammy`
- `mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.5.0-noble`
- `mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.5.0-azurelinux3`

Ubuntu images use `apt-get` to install Lustre client deb packages. The Azure Linux 3 image
uses `tdnf` to install Lustre client RPM packages. The `lustre-loader` sidecar installs the
full `amlfs-lustre-client-*` metapackage (kernel modules + userspace tools), while the
`azurelustre` driver container installs only the userspace tools; both come from the same
package family so kernel modules and userspace tools stay in sync.

## Node pod containers

Every node pod (all three flavors) runs the same four-container layout, and the
same per-flavor image is reused across the first two — the behavior is selected
at runtime via the `AZURELUSTRE_CSI_ROLE` environment variable:

1. **`lustre-loader`** — a native sidecar (an init container with
   `restartPolicy: Always`, `AZURELUSTRE_CSI_ROLE=loader`). Installs the full
   `amlfs-lustre-client-*` metapackage, loads the Lustre kernel modules into the
   shared host kernel, configures LNet, then runs an LNet-config reconcile loop
   for the life of the pod. Its `startupProbe` gates the remaining containers
   until LNet is up, and on termination its `SIGTERM` handler unloads the
   modules so nothing is left behind on the host. The unload is best effort: the
   kernel refuses to remove modules that are still in use, so on a node with a
   mounted Lustre filesystem the modules stay resident and the loader logs a
   `WARNING:`.
2. **`azurelustre`** — the CSI driver (`AZURELUSTRE_CSI_ROLE=driver`). Installs
   only the kernel-agnostic userspace tools, then serves the CSI gRPC socket.
3. **`liveness-probe`** and **`node-driver-registrar`** — the standard
   kubernetes-csi sidecars.

## Troubleshooting

To check which nodes are running which versions:

```bash
kubectl get nodes -o custom-columns=NAME:.metadata.name,OS-SKU:.metadata.labels.'kubernetes\.azure\.com/os-sku-effective'
```

To check DaemonSet pod distribution:

```bash
kubectl get pods -n kube-system -l app=csi-azurelustre-node,flavor=jammy -o wide
kubectl get pods -n kube-system -l app=csi-azurelustre-node,flavor=noble -o wide
kubectl get pods -n kube-system -l app=csi-azurelustre-node,flavor=azurelinux3 -o wide
```
