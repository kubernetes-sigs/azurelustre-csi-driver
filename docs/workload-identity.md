# Workload Identity Support

The Azure Lustre CSI driver supports [Microsoft Entra Workload Identity](https://learn.microsoft.com/en-us/azure/aks/workload-identity-overview) for authenticating to Azure when using dynamic provisioning. When configured, the driver uses a federated identity credential instead of the node's managed identity to make Azure Resource Manager API calls.

> **Note:** Workload Identity is only required for **dynamic provisioning** (creating and deleting Azure Managed Lustre filesystem resources). Static provisioning (mounting an existing AMLFS cluster) uses kernel Lustre mounts over the network and does not require Azure authentication.

## Authentication Modes

The CSI driver uses `DefaultAzureCredential` from the Azure Identity SDK, which supports multiple authentication methods. The deploy manifests include `azure.workload.identity/use: "true"` labels on the pod templates, which enables the AKS workload identity webhook to inject credentials when configured.

### Workload Identity (recommended for AKS)

When the controller service account is annotated with a `client-id`, the AKS webhook injects `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, and `AZURE_FEDERATED_TOKEN_FILE` into the controller pods. `DefaultAzureCredential` detects these and authenticates via `WorkloadIdentityCredential`.

**This is the recommended mode** and is required for AKS extension support.

### Managed Identity (clusters without workload identity)

On AKS clusters created **without** `--enable-workload-identity` (or non-AKS clusters), the workload identity webhook is not present. The pod labels have no effect and no WI env vars are injected. `DefaultAzureCredential` falls back to managed identity using the kubelet identity from `/etc/kubernetes/azure.json`.

> **Important:** On AKS clusters with workload identity **enabled**, the webhook will inject partial env vars even without the `client-id` annotation (it injects `AZURE_TENANT_ID` and `AZURE_FEDERATED_TOKEN_FILE` but not `AZURE_CLIENT_ID`). This causes `DefaultAzureCredential` to attempt workload identity authentication with the kubelet identity, which will fail because the kubelet identity does not have a federated credential. **If your cluster has workload identity enabled, you must complete the setup below** for dynamic provisioning to work.

## Prerequisites

- An AKS cluster with [OIDC issuer](https://learn.microsoft.com/en-us/azure/aks/use-oidc-issuer) and [workload identity](https://learn.microsoft.com/en-us/azure/aks/workload-identity-deploy-cluster) enabled
- A user-assigned managed identity in Azure
- A federated identity credential linking the Kubernetes service account to the managed identity

## Setup

### 1. Enable Workload Identity on Your AKS Cluster

If not already enabled:

```bash
az aks update \
  --resource-group <RESOURCE_GROUP> \
  --name <CLUSTER_NAME> \
  --enable-oidc-issuer \
  --enable-workload-identity
```

### 2. Create a User-Assigned Managed Identity

```bash
az identity create \
  --name <IDENTITY_NAME> \
  --resource-group <RESOURCE_GROUP> \
  --location <LOCATION>

export USER_ASSIGNED_CLIENT_ID="$(az identity show \
  --resource-group <RESOURCE_GROUP> \
  --name <IDENTITY_NAME> \
  --query 'clientId' -o tsv)"
```

### 3. Assign Required Roles

The managed identity needs permissions to manage AMLFS resources. See [driver-parameters.md](driver-parameters.md) for the full list of required permissions.

At minimum, assign:

```bash
# Contributor on the resource group where AMLFS clusters will be created
az role assignment create \
  --assignee "${USER_ASSIGNED_CLIENT_ID}" \
  --role "Contributor" \
  --scope "/subscriptions/<SUBSCRIPTION_ID>/resourceGroups/<RESOURCE_GROUP>"

# Network Contributor on the virtual network
az role assignment create \
  --assignee "${USER_ASSIGNED_CLIENT_ID}" \
  --role "Network Contributor" \
  --scope "/subscriptions/<SUBSCRIPTION_ID>/resourceGroups/<VNET_RESOURCE_GROUP>/providers/Microsoft.Network/virtualNetworks/<VNET_NAME>"
```

### 4. Create a Federated Identity Credential

Get the OIDC issuer URL:

```bash
export AKS_OIDC_ISSUER="$(az aks show \
  --name <CLUSTER_NAME> \
  --resource-group <RESOURCE_GROUP> \
  --query "oidcIssuerProfile.issuerUrl" -o tsv)"
```

Create the federated credential for the controller service account:

```bash
az identity federated-credential create \
  --name "azurelustre-csi-controller" \
  --identity-name <IDENTITY_NAME> \
  --resource-group <RESOURCE_GROUP> \
  --issuer "${AKS_OIDC_ISSUER}" \
  --subject "system:serviceaccount:kube-system:csi-azurelustre-controller-sa" \
  --audience api://AzureADTokenExchange
```

### 5. Annotate the Service Account

The CSI driver's controller service account must be annotated with the managed identity's client ID:

```bash
kubectl annotate serviceaccount csi-azurelustre-controller-sa \
  -n kube-system \
  azure.workload.identity/client-id="${USER_ASSIGNED_CLIENT_ID}"
```

The service account already has the `azure.workload.identity/use: "true"` label in the default deployment manifests, which enables the AKS workload identity webhook to inject the required environment variables and projected token volume.

> **Note:** The node service account (`csi-azurelustre-node-sa`) also carries the workload identity label for consistency, but only the **controller** needs workload identity configuration. The node plugin performs kernel Lustre mounts over the network and does not make Azure API calls. You do not need to create a federated credential or annotation for the node service account.

## How It Works

The deploy manifests include the `azure.workload.identity/use: "true"` label on both the service accounts and the pod templates. On AKS clusters with workload identity enabled, the mutating admission webhook detects this label and injects into pods:

- `AZURE_TENANT_ID` — the tenant ID of the cluster
- `AZURE_CLIENT_ID` — the client ID from the service account's `azure.workload.identity/client-id` annotation
- `AZURE_FEDERATED_TOKEN_FILE` — path to a projected service account token volume
- `AZURE_AUTHORITY_HOST` — the Entra ID authority URL

`DefaultAzureCredential` detects these environment variables and uses `WorkloadIdentityCredential` to exchange the projected service account token for an Entra ID access token via the federated identity credential.

> **Note:** The webhook only injects `AZURE_CLIENT_ID` when the service account has the `azure.workload.identity/client-id` annotation. Without this annotation, the webhook injects the other env vars but not the client ID, which causes authentication to fail. This is why Step 5 (annotating the service account) is required.

## Clusters Without Workload Identity

On AKS clusters created without `--enable-workload-identity`, or on non-AKS Kubernetes clusters, the workload identity webhook is not installed. The `azure.workload.identity/use: "true"` labels on the pod templates have no effect — no env vars or projected volumes are injected. `DefaultAzureCredential` uses the managed identity from `/etc/kubernetes/azure.json` as it always has.

No additional configuration is needed for these clusters — dynamic provisioning works via the kubelet managed identity as documented in [driver-parameters.md](driver-parameters.md).
