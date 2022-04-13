# Azure Lustre CSI Driver for Kubernetes

[![Travis](https://travis-ci.org/kubernetes-sigs/azurelustre-csi-driver.svg)](https://travis-ci.org/kubernetes-sigs/azurelustre-csi-driver)
[![Coverage Status](https://coveralls.io/repos/github/kubernetes-sigs/azurelustre-csi-driver/badge.svg?branch=main)](https://coveralls.io/github/kubernetes-sigs/azurelustre-csi-driver?branch=main)
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fjusjin-org%2Fazurelustre-csi-driver.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fjusjin-org%2Fazurelustre-csi-driver?ref=badge_shield)

### About

This driver allows Kubernetes to access Azure Lustre filesystem.

#### csi plugin name: `azurelustre.csi.azure.com`

### Project status: under early development

### Container Images & Kubernetes Compatibility:

|driver version  |Image                                             | supported k8s version |
|----------------|--------------------------------------------------|-----------------------|
|main branch     |mcr.microsoft.com/k8s/csi/azurelustre-csi:latest  | 1.21+                 |

### Driver parameters

Please refer to `azurelustre.csi.azure.com` [driver parameters](./docs/driver-parameters.md)

### Set up CSI driver on AKS cluster (only for AKS users)

follow guide [here](./docs/install-driver-on-aks.md)

### Usage

- [Basic usage](./deploy/example/e2e_usage.md)
- [fsGroupPolicy](./deploy/example/fsgroup)

### Troubleshooting

- [CSI driver troubleshooting guide](./docs/csi-debug.md)

### Support

- Please see our [support policy][support-policy]

### Limitations

- Please refer to [Azure Lustre CSI Driver Limitations](./docs/limitations.md)

## Kubernetes Development

- Please refer to [development guide](./docs/csi-dev.md)

### View CI Results

- Check testgrid [provider-azure-azurelustre-csi-driver](https://testgrid.k8s.io/provider-azure-azurelustre-csi-driver) dashboard.

### Links

- [Kubernetes CSI Documentation](https://kubernetes-csi.github.io/docs/)
- [CSI Drivers](https://github.com/kubernetes-csi/drivers)
- [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)

[support-policy]: support.md
