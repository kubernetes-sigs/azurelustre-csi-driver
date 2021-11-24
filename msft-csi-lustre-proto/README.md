# msft-csi-lustre-proto

## Introduction

This is a private repo for the LaaSO prototype CSI driver.
The driver is known to function with pre-existing LaaSO clusters.
A single CSI driver binary is used for both the Controller and the Node plugins.

## Folder Structure

* code
  * bin - the location where the built CSI driver binary goes (not in source control).
  * debs - the location where the built Lustre client driver debian packages exist.
  * deploy
    * base
      * controller.yaml - the Controller plugin Deployment configuration.
      * node.yaml - the Node plugin Daemonset configuration.
      * rbac.yaml - the K8S RBAC configuration (different from Azure RBAC).
      * csidriver.yaml - CSI driver configuration.
    * pv
      * pv.yml - definition for Persistent Volume (manually created PVs - **reference only**)
    * sc
      * storageclass.yaml - definition for the Storage Class that can dynamically generate PVs when a PVC is made.
  * example/dynamic_provisioning
    * demo-pod - A Hello World written in C# that would read from the root of a mounted FS.  Crashes in recent memory.
    * ior-pod - A *work in progress* deployment attempting to use `kubeflow` to run ior performance tests.
    * claim1.yaml - PVC example
    * claim2.yaml - PVC example
    * claim3.yaml - PVC example (for project pre-fork)
    * pod.yaml - Pod with 2 containers that read the contents of the Lustre mountpoint.
    Use `kubectl logs -f ...` to see them print.
    Makes a PVC against the Storage Class.
    * pod2.yaml - 1 container version of pod.yaml.
    * pod3.yaml - configuration for demo-pod (not pointing to correct container registry).
  * pkg
    * Dockerfile - the dockerfile for the CSI build container.
    * entrypoint.sh - the command that the CSI container runs.
    * main.go - main function for the actual CSI driver.
      * driver - where the rest of the CSI code lives.
        * controller.go - implements the Controller plugin.
        * driver.go - boilerplate; required.
        * identity.go - implements the Identity portion of the plugin.
        * node.go - implements the Node plugin.
  * Dockerfile - Composes the CSI driver container.
  * Makefile - For building the CSI build container.
  * setup_aks_ssh.sh - Helper script for getting ssh access to K8S cluster nodes.

## Background info for CSI build process

Lustre drivers are completely dependent on the running kernel into which they will be inserted.
This means that the images being used by the AKS team are what we need to target with the driver package.
These images change roughly every two weeks, which means we need to rebuild the drivers (and the CSI that
contains them) every time the image changes.

The longer-term goal here is to track the Ubuntu image that the AKS team uses as their base image.
That base image changes before the AKS team updates their image.  If we determine that the kernel has
changed, then we can pre-build the drivers and keep them in an Artifact feed to be fetched by the CSI
driver daemonset once it loads on a node and checks the running kernel version.  We are not there, yet.
We are also not testing the CSI functionality with any regularity, so this is not a tremendous pain point
(yet).

## CSI Build and Configuration Process

There are multiple steps in the build process.
There a configuration steps that assume you have a pre-existing LaaSO cluster.

### Install az cli program

[az cli](https://docs.microsoft.com/en-us/cli/azure/install-azure-cli)

### Creating a Storage Account

1. Create a storage account in your subscription.  Cheapest settings possible is fine.
2. Create a storage container in that SA.
3. Generate a SAS token for that SA which gives you write permissions to at least that container.

### Building the Lustre Client drivers

1. Create an AKS instance.  Ensure that the system pool is a single D32, or use the default system pool and
   create a secondary user pool with a single D32 VM in it.
   1. Recommend the form alias-YYYY-MM-DD[a-z], where [a-z] represents the generation of VM you've created
   today.
2. After the AKS instance is deployed, edit `setup_aks_ssh.sh`.
   1. Find the block at the top of the file and fill in the appropriate values for the following constants.
      1. `AKS_NAME` - What you named your AKS cluster.
      2. `SCALE_SET_NAME` - There will be an additional resource group created in your sub.  Usually of the
        form `MC_...`.  You have to go into that and find the VMSS name.  This is it.
      3. `SUB` - Your subscription id.  This is a GUID.
      4. `RG` - The resource group in which you created the AKS cluster.
3. Run the newly modified `setup_aks_ssh.sh`.
   1. This will spin for a bit, and drop you into a root shell in a running `ssh` container in the AKS
    cluster.
4. From the root shell: `apt-get update && apt-get install openssh-client -y`.
   1. While this is running, proceed to the next steps.
5. From a DIFFERENT terminal window:
   1. `kubectl get nodes -o wide`
      1. Note IP of node you want to SSH to.
   2. `kubectl cp ~/.ssh/id_rsa $(kubectl get pod -l run=aks-ssh -o jsonpath='{.items[0].metadata.name}'):/id_rsa`
      1. This is copying your private key to the AKS cluster.  This will not be there long.
6. Return to the terminal from step 4.
7. `chmod 0600 id_rsa`
   1. This is setting the proper permissions on your AKS cluster.
8. `ssh -i id_rsa azureuser@[IP from 5.1.1]`
   1. You will be prompted for the password on your laptop's private key file, because now it's in the AKS
    cluster.
9. Now you are ssh'd into the VM on which we will build the Lustre Client drivers.
10. `sudo su -`
11. `apt-get update`
12. `apt install -y linux-image-$(uname -r) libtool m4 autotools-dev automake libelf-dev build-essential debhelper devscripts fakeroot kernel-wedge libudev-dev libpci-dev texinfo xmlto libelf-dev python-dev liblzma-dev libaudit-dev dh-systemd libyaml-dev module-assistant libreadline-dev dpatch libsnmp-dev quilt python3.7 python3.7-dev python3.7-distutils pkg-config libselinux1-dev mpi-default-dev libiberty-dev libpython3.7-dev libpython3-dev swig flex bison`
13. `git clone git://git.whamcloud.com/fs/lustre-release.git`
    1.  We can't easily get access to ADO to use our official repo at this time.
14. `cd lustre-release` (pwd should be `~/lustre-release`)
15. `git checkout 2.14.0`
16. `git reset --hard && git clean -dfx && sh autogen.sh`
17. `./configure --disable-server`
18. `make debs -j 28`
    1. This will take some time.  Resulting packages are in `lustre-release/debs`.
    2. It would be too easy to be able to scp the files off of this VM (you can't).
19. `cd ..` (pwd should be `~`)
20. `wget https://aka.ms/downloadazcopy-v10-linux`
    1. This will give you the URL for the latest azcopy utility.
21. `wget [url from previous step]`
    1. This will download something like: `azcopy_linux_amd64_10.8.0.tar.gz`
22. `tar -zxf [file downloaded in previous step]`
    1. Creates a folder like `azcopy_linux_amd64_10.8.0`
23. `[folder from previous step]/azcopy copy "lustre-release/debs/lustre-client-*"  "https://[name of storage account].blob.core.windows.net/[container name in storage account]/[SASTOKENHERE]"`
    1. If you did this correctly, the .deb files will be in the storage container.
24. Download the .debs from the storage container into `[full path to]/msft-csi-lustre-proto/code/debs`.
25. Destroy the AKS cluster.

### Build the CSI build container

A specialized golang build container is needed in order to build the CSI driver.
This has a specific version of golang in it.  Eventually we'll have this standardized, but for bootstrapping
purposes...

1. From `msft-csi-lustre-proto/code/pkg`: `docker build -t laaso/csi-build:latest .`
2. `docker run -v [full path to]/msft-csi-lustre-proto/code/bin:/host_out laaso/csi-build:latest`

### Build the CSI driver

1. From `[full path to]/msft-csi-lustre-proto/code`: `make IMAGE=laasosandbox.azurecr.io/msft-laaso-lustre-csi image`
2. `az acr login -n laasosandbox.azurecr.io`
3. `docker push laasosandbox.azurecr.io/msft-laaso-lustre-csi:latest`
4. Now the CSI is in a container registry.

### Update the YAML files to point to the right Container Registry

1. Find all instances of `jgalaasocr` and replace them with `laasosandbox`.

## Usage Process

Currently the CSI driver requires an existing LAASO
to exist for an AKS cluster to connect to. The following process
can be followed to get to a point where you have an AKS cluster
with a **PersistentVolumeClaim** that links to a LAASO cluster. 

1. Deploy a LAASO cluster 

    * Take note of the MDS address of the LAASO cluster as this will be 
      used by the CSI drivers to determine how to mount the filesystem

2. Deploy an AKS cluster that is able to ping the LAASO cluster.
   This may mean having to manually change the kube CNI to be in a connected
   subnet or by v-net peering the resource group the AKS cluster is in and
   the resource group LAASO cluster

   You can verify this step by using the `code/setup_aks_ssh.sh` script 
   provided in this repo and then running a ping. 

```bash
$ ./setup_aks_ssh.sh
root@AKS# ping MDS_IP_ADDRESS
```
3. Deploy the CSI driver to the AKS cluster

   With the *MDS IP Address* edit `deploy/kubernetes/sc/storageclass.yaml` to
   have the corrent IP address for the `mds-ip-address` field.

   then run the following `kubectl` commands to deploy the driver and make 
   **PersistentVolumeclaim** 

```bash
kubectl apply -k deploy/kubernetes/base
kubectl apply -f deploy/kubernetes/sc/storageclass.yaml
kubectl apply -f example/dynamic_provisioning/claim.yaml
```

4. You can verify that everything is working by checking to see if the PVC is bound.

```bash
kubectl get pvc
kubectl logs -f ...
```
