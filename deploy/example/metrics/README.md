# Get Prometheus metrics from CSI driver

1. Create `csi-amlfs-controller` service with targetPort `29634`
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/amlfs-csi-driver/master/deploy/example/metrics/csi-amlfs-controller-svc.yaml
```

2. Get ClusterIP of service `csi-amlfs-controller`
```console
$ kubectl get svc csi-amlfs-controller -n kube-system
NAME                  TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)     AGE
csi-amlfs-controller   ClusterIP   10.0.156.8   20.39.0.113   29634/TCP   32m
```

3. Run following command to get cloudprovider_azure metrics
```console
ip=`kubectl get svc csi-amlfs-controller -n kube-system | grep amlfs | awk '{print $4}'`
curl http://$ip:29634/metrics | grep cloudprovider_azure | grep -e sum -e count
```
