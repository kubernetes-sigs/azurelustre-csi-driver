#!/bin/bash

clusterName="$1"
paramFileName='parameters-1.json'

cp parameters.json $paramFileName
sed -i -e "s/CLUSTER_NAME/$clusterName/g" $paramFileName
az deployment group create  --name $clusterName-deployment --resource-group joatzing-aks --template-file template.json --parameters @$paramFileName
