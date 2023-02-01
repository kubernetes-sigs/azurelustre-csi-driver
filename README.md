# Azure Lustre CSI Driver for Kubernetes

[![Travis](https://travis-ci.org/kubernetes-sigs/azurelustre-csi-driver.svg)](https://travis-ci.org/kubernetes-sigs/azurelustre-csi-driver)
[![Coverage Status](https://coveralls.io/repos/github/kubernetes-sigs/azurelustre-csi-driver/badge.svg?branch=main)](https://coveralls.io/github/kubernetes-sigs/azurelustre-csi-driver?branch=main)
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazurelustre-csi-driver.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazurelustre-csi-driver?ref=badge_shield)

### About

This driver allows Kubernetes to access Azure Lustre file system.

- CSI plugin name: `azurelustre.csi.azure.com`
- Project status: under early development

&nbsp;

### Container Images & Kubernetes Compatibility:

| Driver version  | Image                                                         | Supported k8s version | Lustre client version |
|-----------------|---------------------------------------------------------------|-----------------------|-----------------------|
| main branch     | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:latest   | 1.21+                 | 2.15.1                |
| v0.1.5          | mcr.microsoft.com/oss/kubernetes-csi/azurelustre-csi:v0.1.5   | 1.21+                 | 2.15.1                |

&nbsp;

### Set up CSI driver on AKS cluster (only for AKS users)

follow guide [here](./docs/install-csi-driver.md)

- [Deploy workload with Static Provisioning](./docs/static-provisioning.md)

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
