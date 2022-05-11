# Azure azurelustre Storage CSI driver development guide

 - Clone repo
```console
$ mkdir -p $GOPATH/src/sigs.k8s.io
$ git clone https://github.com/kubernetes-sigs/azurelustre-csi-driver $GOPATH/src/sigs.k8s.io/azurelustre-csi-driver
```

 - Build azurelustre Storage CSI driver
```console
$ cd $GOPATH/src/sigs.k8s.io/azurelustre-csi-driver
$ make azurelustre
```

 - Run verification before sending PR
```console
$ make verify
```

 - If there is config file changed under `charts` directory, run following command to update chart file.
```console
helm package charts/latest/azurelustre-csi-driver -d charts/latest/
```

 - Build container image and push to dockerhub
```console
export REGISTRY_NAME=<dockerhub-alias>
make push-latest
```
---
## Test locally using csc tool

### Install CSC
Install `csc` tool according to https://github.com/rexray/gocsi/tree/master/csc:
```console
$ mkdir -p $GOPATH/src/github.com
$ cd $GOPATH/src/github.com
$ git clone https://github.com/rexray/gocsi.git
$ cd rexray/gocsi/csc
$ make build
```

### Setup variables
```console
readonly volname="testvolume-$(date +%s)"
readonly cap="MULTI_NODE_MULTI_WRITER,mount,,,"
readonly target_path="/tmp/lustre-pv"
readonly endpoint="tcp://127.0.0.1:10000"

readonly lustre_fs_name=""
readonly lustre_fs_ip=""
```

### Start CSI driver locally
```console
$ cd $GOPATH/src/sigs.k8s.io/azurelustre-csi-driver
$ ./_output/azurelustreplugin --endpoint $endpoint --nodeid CSINode -v=5 &
```
> Before running CSI driver, create "/etc/kubernetes/azure.json" file under testing server(it's better copy `azure.json` file from a k8s cluster with service principle configured correctly) and set `AZURE_CREDENTIAL_FILE` as following:
```
export set AZURE_CREDENTIAL_FILE=/etc/kubernetes/azure.json
```

#### 1. Get plugin info
```console
$ csc identity plugin-info --endpoint $endpoint
"azurelustre.csi.azure.com"        "v0.1.0"
```

#### 2. Create an azurelustre volume
```console
$ csc controller new --endpoint $endpoint --cap $cap --req-bytes 2147483648 --params "fs-name=$lustre_fs_name,mds-ip-address=$lustre_fs_ip" $volname
```

#### 3. Publish volume
```console
mkdir /tmp/target-path
csc node publish --endpoint $endpoint --cap $cap --target-path $target_path --vol-context "fs-name=$lustre_fs_name,mds-ip-address=$lustre_fs_ip" $volname
```

#### 4. Unpublish volume
```console
csc node unpublish --endpoint $endpoint --target-path $target_path $volname
```


#### 5. Delete azurelustre volume
```console
$ csc controller del --endpoint $endpoint CSIVolumeID
CSIVolumeID
```

#### 6. Validate volume capabilities
```console
$ csc controller validate-volume-capabilities --endpoint $endpoint --cap $cap CSIVolumeID
CSIVolumeID  true
```

#### 7. Get NodeID
```console
$ csc node get-info --endpoint $endpoint
CSINode
```

### How to update chart index

```console
helm repo index charts --url=https://raw.githubusercontent.com/kubernetes-sigs/azurelustre-csi-driver/master/charts
```