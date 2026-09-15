# Workload Identity

The controller can authenticate to Azure with
[Microsoft Entra Workload Identity](https://learn.microsoft.com/en-us/azure/aks/workload-identity-overview)
instead of the node's managed identity when it creates and deletes Azure Managed
Lustre (AMLFS) filesystems (dynamic provisioning). Static provisioning does not
call Azure and is unaffected.

## Setup

These steps configure a direct Helm installation. They require an existing AKS
cluster and the Azure CLI. Fill in the `<PLACEHOLDERS>`.

### 1. Enable OIDC issuer and workload identity on the cluster

```bash
az aks update \
  --resource-group <RESOURCE_GROUP> \
  --name <CLUSTER_NAME> \
  --enable-oidc-issuer \
  --enable-workload-identity
```

### 2. Create a user-assigned managed identity

```bash
az identity create \
  --name <IDENTITY_NAME> \
  --resource-group <RESOURCE_GROUP> \
  --location <LOCATION>

export USER_ASSIGNED_CLIENT_ID="$(az identity show \
  --resource-group <RESOURCE_GROUP> --name <IDENTITY_NAME> \
  --query clientId -o tsv)"
```

### 3. Grant the identity permissions to manage AMLFS

Assign the same permissions the driver otherwise needs on the kubelet identity.
See [Permissions For Kubelet Identity](driver-parameters.md#permissions-for-kubelet-identity)
for the full list of actions and roles.

### 4. Create a federated identity credential for the controller ServiceAccount

```bash
export AKS_OIDC_ISSUER="$(az aks show \
  --resource-group <RESOURCE_GROUP> --name <CLUSTER_NAME> \
  --query oidcIssuerProfile.issuerUrl -o tsv)"

az identity federated-credential create \
  --name azurelustre-csi-controller \
  --identity-name <IDENTITY_NAME> \
  --resource-group <RESOURCE_GROUP> \
  --issuer "${AKS_OIDC_ISSUER}" \
  --subject "system:serviceaccount:kube-system:csi-azurelustre-controller-sa" \
  --audience api://AzureADTokenExchange
```

> The `--subject` must match the controller ServiceAccount exactly. If you install
> into a namespace other than `kube-system`, change it to
> `system:serviceaccount:<namespace>:csi-azurelustre-controller-sa`.
> The ServiceAccount name itself is fixed by the chart and cannot be changed.

### 5. Install/upgrade the chart with the identity's client ID

```bash
helm upgrade --install azurelustre-csi-driver \
  charts/latest/azurelustre-csi-driver \
  --namespace kube-system \
  --set IsWorkloadIdentityEnabled=Enabled \
  --set IdentityClientId="${USER_ASSIGNED_CLIENT_ID}"
# add --set IdentityTenantId="<TENANT_ID>" only for cross-tenant scenarios
```

### 6. Verify

```bash
kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=300 \
  | grep -i "authenticating with"
# expect: authenticating with workload identity (client ID "<client-id>")
```

`AZURE_TOKEN_CREDENTIALS=WorkloadIdentityCredential` restricts the controller to
that one credential, with no managed-identity fallback. If the webhook did not
inject `AZURE_FEDERATED_TOKEN_FILE`, the log reads
`configured for workload identity but AZURE_FEDERATED_TOKEN_FILE is not set`
and Azure calls fail until that is fixed.

Only the controller authenticates to Azure. Node pods never call ARM, so they
build no credential and log no identity line.

## Troubleshooting

See
[Workload identity troubleshooting](csi-debug.md#workload-identity-dynamic-provisioning)
and
[Authentication and Authorization Errors](errors.md#authentication-and-authorization-errors).
