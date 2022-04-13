# Get Prometheus metrics from CSI driver

Step 1. Create `csi-azurelustre-controller` service with targetPort `29634`

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azurelustre-csi-driver/master/deploy/example/metrics/csi-azurelustre-controller-svc.yaml
```

Step 2. Get ClusterIP of service `csi-azurelustre-controller`

```console
$ kubectl get svc csi-azurelustre-controller -n kube-system
NAME                  TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)     AGE
csi-azurelustre-controller   ClusterIP   10.0.156.8   20.39.0.113   29634/TCP   32m
```

Step 3. Run following command to get cloudprovider_azure metrics

```console
ip=`kubectl get svc csi-azurelustre-controller -n kube-system | grep azurelustre | awk '{print $4}'`
curl http://$ip:29634/metrics | grep cloudprovider_azure | grep -e sum -e count
```
