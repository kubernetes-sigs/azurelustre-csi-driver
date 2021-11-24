#!/usr/bin/env bash

export AKS_NAME=jusjin-csi
export SCALE_SET_NAME=aks-user-33352463-vmss
export SUB=1b346619-c3d7-4d15-a9bb-cb3c64a9d6c6
export RG=jusjin-csi

az aks get-credentials --subscription $SUB  --resource-group $RG --name $AKS_NAME
kubectl get nodes -o wide

export CLUSTER_RESOURCE_GROUP=$(az aks show --resource-group $RG --name $AKS_NAME --subscription $SUB --query nodeResourceGroup -o tsv)

az vmss extension set  \
    --subscription $SUB \
    --resource-group $CLUSTER_RESOURCE_GROUP \
    --vmss-name $SCALE_SET_NAME \
    --name VMAccessForLinux \
    --publisher Microsoft.OSTCExtensions \
    --version 1.4 \
    --protected-settings "{\"username\":\"azureuser\", \"ssh_key\":\"$(cat ~/.ssh/id_rsa.pub)\"}"

az vmss update-instances --instance-ids '*' \
    --subscription $SUB \
    --resource-group $CLUSTER_RESOURCE_GROUP \
    --name $SCALE_SET_NAME

kubectl run -it --rm aks-ssh --image=debian

