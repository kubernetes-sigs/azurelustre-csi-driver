## CSI driver troubleshooting guide
### Case#1: volume create/delete issue
 - locate csi driver pod
```console
kubectl get po -o wide -n kube-system | grep csi-azurelustre-controller
```
<pre>
NAME                                       READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azurelustre-controller-56bfddd689-dh5tk       3/3     Running   0          35s     10.240.0.19    k8s-agentpool-22533604-0
csi-azurelustre-controller-56bfddd689-sl4ll       3/3     Running   0          35s     10.240.0.23    k8s-agentpool-22533604-1
</pre>
 - get csi driver logs
```console
kubectl logs csi-azurelustre-controller-56bfddd689-dh5tk -c azurelustre -n kube-system > csi-lustre-controller.log
```
> note: there could be multiple controller pods, logs can be taken from all of them simultaneously, also with `follow` (realtime) mode
> `kubectl logs deploy/csi-azurelustre-controller -c azurelustre -f -n kube-system`

### Case#2: volume mount/unmount failed
 - locate csi driver pod and make sure which pod does the actual volume mount/unmount
```console
kubectl get po -o wide -n kube-system | grep csi-azurelustre-node
```
<pre>
NAME                                       READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azurelustre-node-cvgbs                        3/3     Running   0          7m4s    10.240.0.35    k8s-agentpool-22533604-1
csi-azurelustre-node-dr4s4                        3/3     Running   0          7m4s    10.240.0.4     k8s-agentpool-22533604-0
</pre>

 - get csi driver logs
```console
kubectl logs csi-azurelustre-node-cvgbs -c azurelustre -n kube-system > csi-azurelustre-node.log
```
> note: to watch logs in realtime from multiple `csi-azurelustre-node` DaemonSet pods simultaneously, run the command:
> ```console
> kubectl logs daemonset/csi-azurelustre-node -c azurelustre -n kube-system -f
> ```

 - check Lustre mount inside driver
```console
kubectl exec -it csi-azurelustre-node-9vl9t -n kube-system -c azurelustre -- mount | grep azurelustre
```

#### Update driver version quickly by editing driver deployment directly
 - update controller deployment
```console
kubectl edit deployment csi-azurelustre-controller -n kube-system
```
 - update daemonset deployment
```console
kubectl edit ds csi-azurelustre-node -n kube-system
```
change below deployment config, e.g.
```console
        image: mcr.microsoft.com/k8s/csi/azurelustre-csi:v0.1.0
        imagePullPolicy: Always
```

### get azure lustre driver version
```console
kubectl exec -it csi-azurelustre-node-fmbqw -n kube-system -c azurelustre -- sh
./azurelustreplugin -version
```
<pre>
Build Date: "2022-05-09T02:24:56Z"
Compiler: gc
Driver Name: azurelustre.csi.azure.com
Driver Version: v0.1.1
Git Commit: 43cc3815d103eebf1ab0b34c6b0b57a4d48e2f4b
Go Version: go1.17.3
Platform: linux/amd64
</pre>
