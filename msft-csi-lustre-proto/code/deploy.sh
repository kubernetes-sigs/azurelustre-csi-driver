#!/usr/bin/env bash

# This script will take in an existing LAASO cluster and an
# existing AKS cluster and connect them together using
# the CSI drivers in this repository

AKS_NAME=
AKS_RG=
AKS_SUB=
LAASO_NAME=
LAASO_SUB=
LAASO_RG=

# Get credentials for AKS cluster and load them into the .kube configuration of your system
az aks get-credentials --subscription $AKS_SUB --resource-group $AKS_RG --name $AKS_NAME

# TODO get MDS address for laaso cluster and edit the Storage Class


# deploy CSI drivers

kubectl apply -k deploy/kubernetes/base
kubectl apply -f deploy/kubernetes/sc/storageclass.yaml
kubectl apply -f example/dynamic_provisioning/claim.yaml

# Verify 
kubectl get pvc
