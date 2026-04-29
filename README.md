# Azure Lustre CSI Driver for Kubernetes

[![Coverage Status](https://coveralls.io/repos/github/kubernetes-sigs/azurelustre-csi-driver/badge.svg?branch=main)](https://coveralls.io/github/kubernetes-sigs/azurelustre-csi-driver?branch=main)
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazurelustre-csi-driver.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazurelustre-csi-driver?ref=badge_shield)

### About

This driver allows Kubernetes to access Azure Lustre file system.

- CSI plugin name: `azurelustre.csi.azure.com`
- Project status: under early development

&nbsp;

### Container Images & Kubernetes Compatibility

Starting with v0.4.0, the driver ships separate images per Ubuntu distribution: `-jammy` (22.04) and `-noble` (24.04). See [deploy/README-distribution-specific.md](deploy/README-distribution-specific.md) for details.

| Driver version | Image | Supported k8s version | Lustre client version |
| -------------- | ----- | --------------------- | --------------------- |
| main branch | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-jammy<br>mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-noble | 1.21+ | 2.15.7 (jammy) / 2.16.1 (noble) |
| development branch | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:latest-jammy<br>mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:latest-noble | 1.21+ | 2.15.8¹ (jammy) / 2.16.1 (noble) |
| v0.4.0 | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-jammy<br>mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-noble | 1.21+ | 2.15.7 |
| v0.3.1 | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.3.1 | 1.21+ | 2.15.7 |
| v0.3.0 | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.3.0 | 1.21+ | 2.15.5 |
| v0.2.0 | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.2.0 | 1.21+ | 2.15.5 |
| v0.1.18 | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.18 | 1.21+ | 2.15.5 |
| v0.1.17 | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.17 | 1.21+ | 2.15.5 |
| v0.1.15 | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.15 | 1.21+ | 2.15.4 |
| v0.1.14 | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.14 | 1.21+ | 2.15.3 |
| v0.1.11 | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.11 | 1.21+ | 2.15.1 |

¹ Lustre 2.15.8 introduces the `unique_fsid` mount option, automatically enabled by the driver. This gives each volume mount its own filesystem ID, allowing Kubernetes to properly manage multiple Lustre volumes on a single node. See [driver-parameters.md](docs/driver-parameters.md#unique-filesystem-id-unique_fsid) for details.

&nbsp;

### Set up CSI driver on AKS cluster (only for AKS users)

- [Install CSI driver in AKS cluster](./docs/install-csi-driver.md)
- [Deploy workload with Static Provisioning](./docs/static-provisioning.md)
- [Deploy workload with Dynamic Provisioning](./docs/dynamic-provisioning.md)

&nbsp;

### Troubleshooting

- [CSI driver troubleshooting guide](./docs/csi-debug.md)

&nbsp;

### Support

- Please see our [support policy][support-policy]

&nbsp;

## Kubernetes Development

- Please refer to [development guide](./docs/csi-dev.md)

&nbsp;

### Links

- [Kubernetes CSI Documentation](https://kubernetes-csi.github.io/docs/)
- [CSI Drivers](https://github.com/kubernetes-csi/drivers)
- [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)

[support-policy]: support.md
