# Azure Lustre CSI Driver for Kubernetes

[![Coverage Status](https://coveralls.io/repos/github/kubernetes-sigs/azurelustre-csi-driver/badge.svg?branch=main)](https://coveralls.io/github/kubernetes-sigs/azurelustre-csi-driver?branch=main)
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazurelustre-csi-driver.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazurelustre-csi-driver?ref=badge_shield)

## About

This driver allows Kubernetes to access Azure Lustre file system.

- CSI plugin name: `azurelustre.csi.azure.com`
- Project status: under early development

&nbsp;

## Container Images & Kubernetes Compatibility

Starting with v0.4.0, the driver ships separate images per node OS: `-jammy` (Ubuntu 22.04) and `-noble` (Ubuntu 24.04). An `-azurelinux3` (Azure Linux 3) image is currently available on the development branch. See [deploy/README-distribution-specific.md](deploy/README-distribution-specific.md) for details.

The development branch requires Kubernetes 1.29+ because the node plugin uses native sidecar containers (init containers with `restartPolicy: Always`), enabled by default from 1.29. The main branch and released versions support 1.21+.

| Driver version | Image | Supported k8s version | Lustre client version |
| -------------- | ----- | --------------------- | --------------------- |
| main branch | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-jammy<br>mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-noble | 1.21+ | 2.15.7 (jammy)<br>2.16.1 (noble) |
| development branch | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:latest-jammy<br>mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:latest-noble<br>mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:latest-azurelinux3 | 1.29+ | 2.15.8 (jammy)<br>2.17.0 (noble)<br>2.17.0 (azurelinux3) |
| v0.4.0 | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-jammy<br>mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.4.0-noble | 1.21+ | 2.15.7 (jammy)<br>2.16.1 (noble) |
| v0.3.1 | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.3.1 | 1.21+ | 2.15.7 |
| v0.3.0 | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.3.0 | 1.21+ | 2.15.5 |
| v0.2.0 | mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:v0.2.0 | 1.21+ | 2.15.5 |
| v0.1.18 | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.18 | 1.21+ | 2.15.5 |
| v0.1.17 | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.17 | 1.21+ | 2.15.5 |
| v0.1.15 | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.15 | 1.21+ | 2.15.4 |
| v0.1.14 | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.14 | 1.21+ | 2.15.3 |
| v0.1.11 | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.11 | 1.21+ | 2.15.1 |

&nbsp;

## Set up CSI driver on AKS cluster (only for AKS users)

- [Install CSI driver in AKS cluster](./docs/install-csi-driver.md)
- [Deploy workload with Static Provisioning](./docs/static-provisioning.md)
- [Deploy workload with Dynamic Provisioning](./docs/dynamic-provisioning.md)

&nbsp;

## Troubleshooting

- [CSI driver troubleshooting guide](./docs/csi-debug.md)

&nbsp;

## Support

- Please see our [support policy][support-policy]

&nbsp;

## Kubernetes Development

- Please refer to [development guide](./docs/csi-dev.md)

&nbsp;

## Links

- [Kubernetes CSI Documentation](https://kubernetes-csi.github.io/docs/)
- [CSI Drivers](https://github.com/kubernetes-csi/drivers)
- [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)

[support-policy]: support.md
