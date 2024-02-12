/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package azurelustre

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"
	csicommon "sigs.k8s.io/azurelustre-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/util"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/configloader"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const (
	// DefaultDriverName holds the name of the csi-driver
	DefaultDriverName        = "azurelustre.csi.azure.com"
	DefaultLustreFsName      = "lustrefs"
	azureLustreCSIDriverName = "azurelustre_csi_driver"
	separator                = "#"
	volumeIDTemplate         = "%s#%s#%s#%s#%s#%s"
	subnetTemplate           = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/%s/subnets/%s"

	amlFilesystemNameMaxLength = 80

	podNameKey            = "csi.storage.k8s.io/pod.name"
	podNamespaceKey       = "csi.storage.k8s.io/pod.namespace"
	podUIDKey             = "csi.storage.k8s.io/pod.uid"
	serviceAccountNameKey = "csi.storage.k8s.io/serviceaccount.name"
	pvcNameKey            = "csi.storage.k8s.io/pvc/name"
	pvcNamespaceKey       = "csi.storage.k8s.io/pvc/namespace"
	pvNameKey             = "csi.storage.k8s.io/pv/name"

	podNameMetadata            = "${pod.metadata.name}"
	podNamespaceMetadata       = "${pod.metadata.namespace}"
	podUIDMetadata             = "${pod.metadata.uid}"
	serviceAccountNameMetadata = "${serviceAccount.metadata.name}"
	pvcNameMetadata            = "${pvc.metadata.name}"
	pvcNamespaceMetadata       = "${pvc.metadata.namespace}"
	pvNameMetadata             = "${pv.metadata.name}"
)

var (
	controllerServiceCapabilities = []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
	}

	volumeCapabilities = []csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
	}

	nodeServiceCapabilities = []csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		csi.NodeServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
	}

	DefaultSkuValues = map[string]*LustreSkuValue{
		"AMLFS-Durable-Premium-40":  {IncrementInTib: 48, MaximumInTib: 768},
		"AMLFS-Durable-Premium-125": {IncrementInTib: 16, MaximumInTib: 128},
		"AMLFS-Durable-Premium-250": {IncrementInTib: 8, MaximumInTib: 128},
		"AMLFS-Durable-Premium-500": {IncrementInTib: 4, MaximumInTib: 128},
	}
)

type lustreVolume struct {
	name                         string
	id                           string
	mgsIPAddress                 string
	azureLustreName              string
	subDir                       string
	createdByDynamicProvisioning bool
	resourceGroupName            string
}

// DriverOptions defines driver parameters specified in driver deployment
type DriverOptions struct {
	NodeID                       string
	DriverName                   string
	EnableAzureLustreMockMount   bool
	EnableAzureLustreMockDynProv bool
	WorkingMountDir              string
}

// LustreSkuValue describes the increment and maximum size of a given Lustre sku
type LustreSkuValue struct {
	IncrementInTib int64
	MaximumInTib   int64
}

// Driver implements all interfaces of CSI drivers
type Driver struct {
	csicommon.CSIDriver
	csicommon.DefaultIdentityServer
	csicommon.DefaultControllerServer
	csicommon.DefaultNodeServer
	// enableAzureLustreMockMount is only for testing, DO NOT set as true in non-testing scenario
	enableAzureLustreMockMount bool
	// enableAzureLustreMockDynProv is only for testing, DO NOT set as true in non-testing scenario
	enableAzureLustreMockDynProv bool
	mounter                      *mount.SafeFormatAndMount // TODO_JUSJIN: check any other alternatives
	forceMounter                 *mount.MounterForceUnmounter
	volLockMap                   *util.LockMap
	// Directory to temporarily mount to for subdirectory creation
	workingMountDir string
	// A map storing all volumes with ongoing operations so that additional operations
	// for that same volume (as defined by VolumeID) return an Aborted error
	volumeLocks      *volumeLocks
	kernelModuleLock sync.Mutex

	cloud              *azure.Cloud
	resourceGroup      string
	location           string
	dynamicProvisioner DynamicProvisionerInterface
}

// NewDriver Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func NewDriver(options *DriverOptions) *Driver {
	d := Driver{
		volLockMap:                   util.NewLockMap(),
		volumeLocks:                  newVolumeLocks(),
		enableAzureLustreMockMount:   options.EnableAzureLustreMockMount,
		enableAzureLustreMockDynProv: options.EnableAzureLustreMockDynProv,
		workingMountDir:              options.WorkingMountDir,
	}
	d.Name = options.DriverName
	d.Version = driverVersion
	d.NodeID = options.NodeID

	d.DefaultControllerServer.Driver = &d.CSIDriver
	d.DefaultIdentityServer.Driver = &d.CSIDriver
	d.DefaultNodeServer.Driver = &d.CSIDriver

	ctx := context.Background()

	// Will need to change if we ever support non-AKS clusters
	AKSConfigFile := "/etc/kubernetes/azure.json"

	az := &azure.Cloud{}
	config, err := configloader.Load[azure.Config](ctx, nil, &configloader.FileLoaderConfig{
		FilePath: AKSConfigFile,
	})
	if err != nil {
		klog.V(2).Infof("failed to get cloud config from file %s: %v", AKSConfigFile, err)
	}

	if config == nil {
		if d.enableAzureLustreMockDynProv {
			klog.V(2).Infof("no cloud config provided, driver running with mock dynamic provisioning")
			d.dynamicProvisioner = &DynamicProvisioner{}
			d.cloud = az
		} else {
			klog.Fatalf("no cloud config provided, error")
		}
	} else {
		config.UserAgent = GetUserAgent(d.Name, "", "")
		if clientID := os.Getenv("AZURE_CLIENT_ID"); clientID != "" {
			config.AADClientID = clientID
		} else if config.UseManagedIdentityExtension && config.UserAssignedIdentityID != "" {
			os.Setenv("AZURE_CLIENT_ID", config.UserAssignedIdentityID)
			config.AADClientID = config.UserAssignedIdentityID
		}
		if err = az.InitializeCloudFromConfig(ctx, config, false, false); err != nil {
			klog.Warningf("InitializeCloudFromConfig failed with error: %v", err)
		}
		d.cloud = az
		d.resourceGroup = config.ResourceGroup
		d.location = config.Location

		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			klog.Warningf("failed to obtain a credential: %v", err)
		}
		storageClientFactory, err := armstoragecache.NewClientFactory(config.SubscriptionID, cred, nil)
		if err != nil {
			klog.Warningf("failed to create storage client factory: %v", err)
		}
		subsID := d.cloud.SubscriptionID
		if len(d.cloud.NetworkResourceSubscriptionID) > 0 {
			subsID = d.cloud.NetworkResourceSubscriptionID
		}
		networkClientFactory, err := armnetwork.NewClientFactory(subsID, cred, nil)
		if err != nil {
			klog.Warningf("failed to create network client factory: %v", err)
		}
		vnetClient := networkClientFactory.NewVirtualNetworksClient()
		skusClient := storageClientFactory.NewSKUsClient()
		mgmtClient := storageClientFactory.NewManagementClient()
		amlFilesystemsClient := storageClientFactory.NewAmlFilesystemsClient()
		d.dynamicProvisioner = &DynamicProvisioner{
			amlFilesystemsClient: amlFilesystemsClient,
			mgmtClient:           mgmtClient,
			vnetClient:           vnetClient,
			skusClient:           skusClient,
			defaultSkuValues:     DefaultSkuValues,
		}
	}

	return &d
}

func (d *Driver) populateSubnetPropertiesFromCloudConfig(subnetInfo SubnetProperties) SubnetProperties {
	subnetProperties := subnetInfo
	subsID := d.cloud.SubscriptionID
	if len(d.cloud.NetworkResourceSubscriptionID) > 0 {
		subsID = d.cloud.NetworkResourceSubscriptionID
	}

	if len(subnetInfo.VnetResourceGroup) == 0 {
		subnetProperties.VnetResourceGroup = d.cloud.ResourceGroup
		if len(d.cloud.VnetResourceGroup) > 0 {
			subnetProperties.VnetResourceGroup = d.cloud.VnetResourceGroup
		}
	}

	if len(subnetInfo.VnetName) == 0 {
		subnetProperties.VnetName = d.cloud.VnetName
	}

	if len(subnetInfo.SubnetName) == 0 {
		subnetProperties.SubnetName = d.cloud.SubnetName
	}
	subnetID := fmt.Sprintf(subnetTemplate, subsID, subnetProperties.VnetResourceGroup, subnetProperties.VnetName, subnetProperties.SubnetName)

	subnetProperties.SubnetID = subnetID

	return subnetProperties
}

// Run driver initialization
func (d *Driver) Run(endpoint string, testBool bool) {
	versionMeta, err := GetVersionYAML(d.Name)
	if err != nil {
		klog.Fatalf("%v", err)
	}
	klog.Infof("\nDRIVER INFORMATION:\n-------------------\n%s\n\nStreaming logs below:", versionMeta)

	d.mounter = &mount.SafeFormatAndMount{
		Interface: mount.New(""),
		Exec:      utilexec.New(),
	}
	forceUnmounter, ok := d.mounter.Interface.(mount.MounterForceUnmounter)
	if ok {
		klog.V(4).Infof("Using force unmounter interface")
		d.forceMounter = &forceUnmounter
	} else {
		klog.Fatalf("Mounter does not support force unmount")
	}

	// TODO_JUSJIN: revisit these caps
	// Initialize default library driver
	// TODO_CHYIN: move this to {service}.go
	d.AddControllerServiceCapabilities(controllerServiceCapabilities)
	d.AddVolumeCapabilityAccessModes(volumeCapabilities)
	d.AddNodeServiceCapabilities(nodeServiceCapabilities)

	s := csicommon.NewNonBlockingGRPCServer()
	// Driver d act as IdentityServer, ControllerServer and NodeServer
	s.Start(endpoint, d, d, d, testBool)
	s.Wait()
}

func IsCorruptedDir(dir string) bool {
	_, pathErr := mount.PathExists(dir)
	return pathErr != nil && mount.IsCorruptedMnt(pathErr)
}

func getLustreVolFromID(id string) (*lustreVolume, error) {
	segments := strings.Split(id, separator)
	if len(segments) < 3 {
		return nil, fmt.Errorf("could not split volume ID %q into lustre name and ip address", id)
	}

	name := segments[0]
	vol := &lustreVolume{
		name:            name,
		id:              id,
		azureLustreName: DefaultLustreFsName,
		mgsIPAddress:    segments[2],
	}

	if len(segments) >= 4 {
		vol.subDir = strings.Trim(segments[3], "/")
	}

	if len(segments) >= 5 {
		vol.createdByDynamicProvisioning = segments[4] == "t"
	}

	if len(segments) >= 6 {
		vol.resourceGroupName = segments[5]
	}

	return vol, nil
}
