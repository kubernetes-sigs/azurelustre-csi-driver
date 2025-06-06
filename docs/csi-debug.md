## CSI driver troubleshooting guide

&nbsp;

### Case#1: volume create/delete issue

&nbsp;

- Symptoms
  - PVC can't go into Bound status
  - User workload pod can't go into a Running status

&nbsp;

- Locate csi driver pod

```console
$ kubectl get po -o wide -n kube-system -l app=csi-azurelustre-controller
```

<pre>
NAME                                              READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azurelustre-controller-56bfddd689-dh5tk       3/3     Running   0          35s     10.240.0.19    k8s-agentpool-22533604-0
csi-azurelustre-controller-56bfddd689-sl4ll       3/3     Running   0          35s     10.240.0.23    k8s-agentpool-22533604-1
</pre>

&nbsp;

- Get csi driver logs

```console
$ kubectl logs csi-azurelustre-controller-56bfddd689-dh5tk -c azurelustre -n kube-system > csi-lustre-controller.log
```

> note:
>
> - add --previous to retrieve logs from a previous running container
>
> - there could be multiple controller pods, logs can be taken from all of them simultaneously
>
> ```console
> $ kubectl logs -n kube-system -l app=csi-azurelustre-controller -c azurelustre --tail=-1 --prefix 
> ```
>
> - retrieve logs with `follow` (realtime) mode
>
> ```console
> $ kubectl logs deploy/csi-azurelustre-controller -c azurelustre -f -n kube-system
> ```

&nbsp;

### Case#2: volume mount/unmount issue

- Locate csi driver pod and find out the pod does the actual volume mount/unmount operation

```console
$ kubectl get po -o wide -n kube-system -l app=csi-azurelustre-node
```

<pre>
NAME                           READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azurelustre-node-9ds7f     3/3     Running   0          7m4s    10.240.0.35    k8s-agentpool-22533604-1
csi-azurelustre-node-dr4s4     3/3     Running   0          7m4s    10.240.0.4     k8s-agentpool-22533604-0
</pre>

&nbsp;

- Get csi driver logs

```console
$ kubectl logs csi-azurelustre-node-9ds7f -c azurelustre -n kube-system > csi-azurelustre-node.log
```

> note: to watch logs in realtime from multiple `csi-azurelustre-node` DaemonSet pods simultaneously, run the command:
>
> ```console
> $ kubectl logs daemonset/csi-azurelustre-node -c azurelustre -n kube-system -f
> ```

&nbsp;

- Check Lustre mounts inside driver

```console
$ kubectl exec -it csi-azurelustre-node-9ds7f -n kube-system -c azurelustre -- mount | grep lustre
```

<pre>
172.18.8.12@tcp:/lustrefs on /var/lib/kubelet/pods/6632349a-05fd-466f-bc8a-8946617089ce/volumes/kubernetes.io~csi/pvc-841498d9-fa63-418c-8cc7-d94ec27f2ee2/mount type lustre (rw,flock,lazystatfs,encrypt)
172.18.8.12@tcp:/lustrefs on /var/lib/kubelet/pods/6632349a-05fd-466f-bc8a-8946617089ce/volumes/kubernetes.io~csi/pvc-841498d9-fa63-418c-8cc7-d94ec27f2ee2/mount type lustre (rw,flock,lazystatfs,encrypt)
</pre>

&nbsp;
&nbsp;

### Case#3: Updating from versions below v0.1.10 to v0.2.0 or later

In-place updates are not supported from any versions below v0.1.10 to v0.2.0 or later. Ensure that you are installing the CSI driver into a fresh node pool to avoid any issues.


&nbsp;
&nbsp;

### Update driver version quickly by editing driver deployment directly

&nbsp;

- Update controller deployment

```console
$ kubectl edit deployment csi-azurelustre-controller -n kube-system
```

&nbsp;

- Update daemonset deployment

```console
$ kubectl edit ds csi-azurelustre-node -n kube-system
```

&nbsp;

- Change lustre CSI docker image config

```console
image: mcr.microsoft.com/k8s/csi/azurelustre-csi:v0.1.0
imagePullPolicy: Always
```

&nbsp;
&nbsp;

### Get azure lustre driver version

```console
$ kubectl exec -it csi-azurelustre-node-9ds7f -n kube-system -c azurelustre -- /bin/bash -c "./azurelustreplugin -version"
```

<pre>
Build Date: "2022-05-11T10:25:15Z"
Compiler: gc
Driver Name: azurelustre.csi.azure.com
Driver Version: v0.1.0
Git Commit: 43017c96b7cecaa09bc05ce9fad3fb9860a4c0ce
Go Version: go1.18.1
Platform: linux/amd64
</pre>

&nbsp;
&nbsp;

### Collect logs for Lustre CSI Driver Product Team for further investigation

&nbsp;

- get utility from /utils/azurelustre_log.sh, run it and share output lustre.logs with us
  
```console
$ chmod +x ./azurelustre_log.sh
$ ./azurelustre_log.sh > lustre.logs 2>&1
```
