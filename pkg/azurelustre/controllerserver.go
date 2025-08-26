/*
Copyright 2017 The Kubernetes Authors.

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
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/util"
	"sigs.k8s.io/cloud-provider-azure/pkg/metrics"
)

const (
	VolumeContextMGSIPAddress               = "mgs-ip-address"
	VolumeContextFSName                     = "fs-name"
	VolumeContextSubDir                     = "sub-dir"
	VolumeContextLocation                   = "location"
	VolumeContextResourceGroupName          = "resource-group-name"
	VolumeContextVnetResourceGroup          = "vnet-resource-group"
	VolumeContextVnetName                   = "vnet-name"
	VolumeContextSubnetName                 = "subnet-name"
	VolumeContextMaintenanceDayOfWeek       = "maintenance-day-of-week"
	VolumeContextMaintenanceTimeOfDayUtc    = "maintenance-time-of-day-utc"
	VolumeContextSkuName                    = "sku-name"
	VolumeContextZone                       = "zone"
	VolumeContextZonesSynonym               = "zones"
	VolumeContextTags                       = "tags"
	VolumeContextIdentities                 = "identities"
	VolumeContextInternalDynamicallyCreated = "created-by-dynamic-provisioning"
	defaultSizeInBytes                      = 4 * util.TiB
	defaultLaaSOBlockSizeInTib              = 4
	pvcNamespaceTag                         = "kubernetes.io-created-for-pvc-namespace"
	pvcNameTag                              = "kubernetes.io-created-for-pvc-name"
	pvNameTag                               = "kubernetes.io-created-for-pv-name"
	createdByTag                            = "k8s-azure-created-by"
	azureLustreDriverTag                    = "kubernetes-azurelustre-csi-driver"
)

var (
	timeRegexp             = regexp.MustCompile(`^([01]?[0-9]|2[0-3]):[0-5][0-9]$`)
	amlFilesystemNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,78}[a-zA-Z0-9]$`)
)

type SubnetProperties struct {
	SubnetID          string
	VnetName          string
	VnetResourceGroup string
	SubnetName        string
}

type AmlFilesystemProperties struct {
	ResourceGroupName    string
	AmlFilesystemName    string
	Location             string
	Tags                 map[string]string
	Identities           []string // Can only be "UserAssigned" identity
	SubnetInfo           SubnetProperties
	MaintenanceDayOfWeek armstoragecache.MaintenanceDayOfWeekType
	TimeOfDayUTC         string
	StorageCapacityTiB   float32
	SKUName              string
	Zone                 string
}

func parseAmlFilesystemProperties(properties map[string]string) (*AmlFilesystemProperties, error) {
	var amlFilesystemProperties AmlFilesystemProperties
	var errorParameters []string

	shouldCreateAmlfsCluster := true
	amlFilesystemProperties.Tags = map[string]string{
		createdByTag: azureLustreDriverTag,
	}

	for propertyName, propertyValue := range properties {
		switch strings.ToLower(propertyName) {
		case VolumeContextResourceGroupName:
			amlFilesystemProperties.ResourceGroupName = propertyValue
		case VolumeContextMGSIPAddress:
			shouldCreateAmlfsCluster = false
		case VolumeContextLocation:
			amlFilesystemProperties.Location = propertyValue
		case VolumeContextVnetName:
			amlFilesystemProperties.SubnetInfo.VnetName = propertyValue
		case VolumeContextVnetResourceGroup:
			amlFilesystemProperties.SubnetInfo.VnetResourceGroup = propertyValue
		case VolumeContextSubnetName:
			amlFilesystemProperties.SubnetInfo.SubnetName = propertyValue
		case VolumeContextMaintenanceDayOfWeek:
			possibleDayValues := armstoragecache.PossibleMaintenanceDayOfWeekTypeValues()
			for _, dayOfWeekValue := range possibleDayValues {
				if string(dayOfWeekValue) == propertyValue {
					amlFilesystemProperties.MaintenanceDayOfWeek = dayOfWeekValue
					break
				}
			}
			if len(amlFilesystemProperties.MaintenanceDayOfWeek) == 0 {
				return nil, status.Errorf(
					codes.InvalidArgument,
					"CreateVolume Parameter %s must be one of: %v",
					VolumeContextMaintenanceDayOfWeek,
					possibleDayValues,
				)
			}
		case VolumeContextMaintenanceTimeOfDayUtc:
			if !timeRegexp.MatchString(propertyValue) {
				return nil, status.Errorf(
					codes.InvalidArgument,
					"CreateVolume Parameter %s must be in the form HH:MM, was: '%s'",
					VolumeContextMaintenanceTimeOfDayUtc,
					propertyValue,
				)
			}
			amlFilesystemProperties.TimeOfDayUTC = propertyValue
		case VolumeContextSkuName:
			amlFilesystemProperties.SKUName = propertyValue
		case VolumeContextZone, VolumeContextZonesSynonym:
			amlFilesystemProperties.Zone = propertyValue
		case VolumeContextTags:
			tags, err := util.ConvertTagsToMap(propertyValue)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "CreateVolume %v", err)
			}
			if len(tags) > 0 {
				for tag, value := range tags {
					if tag == pvcNameTag || tag == pvcNamespaceTag || tag == pvNameTag || tag == createdByTag {
						return nil, status.Errorf(codes.InvalidArgument, "CreateVolume Parameter %s must not contain %s as a tag", VolumeContextTags, tag)
					}
					amlFilesystemProperties.Tags[tag] = value
				}
			}
		case pvcNameKey:
			amlFilesystemProperties.Tags[pvcNameTag] = propertyValue
		case pvcNamespaceKey:
			amlFilesystemProperties.Tags[pvcNamespaceTag] = propertyValue
		case pvNameKey:
			amlFilesystemProperties.Tags[pvNameTag] = propertyValue
		case VolumeContextIdentities:
			amlFilesystemProperties.Identities = strings.Split(propertyValue, ",")
			// These will be used by the node methods
		case VolumeContextFSName, VolumeContextSubDir:
			continue
		default:
			errorParameters = append(
				errorParameters,
				fmt.Sprintf("%s = %s", propertyName, propertyValue),
			)
		}
	}

	if len(errorParameters) > 0 {
		return nil, status.Error(
			codes.InvalidArgument,
			fmt.Sprintf("Invalid parameter(s) {%s} in storage class",
				strings.Join(errorParameters, ", ")),
		)
	}

	if shouldCreateAmlfsCluster {
		if len(amlFilesystemProperties.MaintenanceDayOfWeek) == 0 {
			return nil, status.Errorf(codes.InvalidArgument,
				"CreateVolume %s must be provided for dynamically provisioned AMLFS",
				VolumeContextMaintenanceDayOfWeek)
		}

		if len(amlFilesystemProperties.SKUName) == 0 {
			return nil, status.Errorf(codes.InvalidArgument,
				"CreateVolume %s must be provided for dynamically provisioned AMLFS",
				VolumeContextSkuName)
		}

		if len(amlFilesystemProperties.TimeOfDayUTC) == 0 {
			return nil, status.Errorf(codes.InvalidArgument,
				"CreateVolume %s must be provided for dynamically provisioned AMLFS",
				VolumeContextMaintenanceTimeOfDayUtc)
		}
	}

	return &amlFilesystemProperties, nil
}

func isValidVolumeName(volName string) bool {
	validAmlFilesystemName := volName
	if !amlFilesystemNameRegex.MatchString(validAmlFilesystemName) {
		klog.Warningf("the requested volume name (%q) is invalid", validAmlFilesystemName)
		return false
	}
	return true
}

func validateVolumeCapabilities(capabilities []*csi.VolumeCapability) error {
	for _, capability := range capabilities {
		if capability.GetMount() == nil {
			// Lustre just support mount type. i.e. block type is unsupported.
			return status.Error(codes.InvalidArgument,
				"Doesn't support block volume.")
		}
		support := slices.Contains(volumeCapabilities, capability.GetAccessMode().GetMode())
		if !support {
			return status.Error(codes.InvalidArgument,
				"Volume doesn't support "+
					capability.GetAccessMode().GetMode().String())
		}
	}
	return nil
}

// CreateVolume provisions a volume
func (d *Driver) CreateVolume(
	ctx context.Context,
	req *csi.CreateVolumeRequest,
) (*csi.CreateVolumeResponse, error) {
	mc := metrics.NewMetricContext(
		azureLustreCSIDriverName,
		"controller_create_volume",
		d.resourceGroup,
		d.cloud.SubscriptionID,
		d.Name,
	)

	volName := req.GetName()
	if len(volName) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"CreateVolume Name must be provided")
	}

	err := checkVolumeRequest(req)
	if err != nil {
		return nil, err
	}

	if acquired := d.volumeLocks.TryAcquire(volName); !acquired {
		return nil, status.Errorf(codes.Aborted,
			volumeOperationAlreadyExistsFmt,
			volName)
	}
	defer d.volumeLocks.Release(volName)

	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	parameters := req.GetParameters()
	if parameters == nil {
		return nil, status.Error(codes.InvalidArgument,
			"CreateVolume Parameters must be provided")
	}

	shouldCreateAmlfsCluster := false

	mgsIPAddress := util.GetValueInMap(parameters, VolumeContextMGSIPAddress)
	if mgsIPAddress == "" {
		shouldCreateAmlfsCluster = true
	}

	// Check parameters to ensure validity of static and dynamic configs
	amlFilesystemProperties, err := parseAmlFilesystemProperties(parameters)
	if err != nil {
		return nil, err
	}

	capacityRange := req.GetCapacityRange()

	capacityInBytes := capacityRange.GetRequiredBytes()
	if capacityInBytes == 0 {
		capacityInBytes = defaultSizeInBytes
		klog.V(2).Infof("using default capacity: %#v", capacityInBytes)
	}

	blockSizeInBytes := int64(defaultLaaSOBlockSizeInTib) * util.TiB
	maxCapacityInBytes := int64(0)

	createdByDynamicProvisioningStringValue := "f"

	availableZones := []string{}

	if shouldCreateAmlfsCluster {
		createdByDynamicProvisioningStringValue = "t"

		if len(amlFilesystemProperties.Location) == 0 {
			amlFilesystemProperties.Location = d.location
		}

		if len(amlFilesystemProperties.ResourceGroupName) == 0 {
			amlFilesystemProperties.ResourceGroupName = d.resourceGroup
		}

		amlFilesystemProperties.SubnetInfo = d.populateSubnetPropertiesFromCloudConfig(amlFilesystemProperties.SubnetInfo)

		klog.V(2).Infof("finding capacity based on SKU %s for location %s", amlFilesystemProperties.SKUName, amlFilesystemProperties.Location)
		lustreSkuValue, err := d.getSkuValuesForLocation(ctx, amlFilesystemProperties.SKUName, amlFilesystemProperties.Location)
		if err != nil {
			klog.Errorf("failed to get SKU values for %s in location %s, error: %v", amlFilesystemProperties.SKUName, amlFilesystemProperties.Location, err)
			return nil, err
		}
		blockSizeInBytes = lustreSkuValue.IncrementInTib * util.TiB
		maxCapacityInBytes = lustreSkuValue.MaximumInTib * util.TiB
		availableZones = lustreSkuValue.AvailableZones
	}

	capacityInBytes, err = d.roundToAmlfsBlockSize(capacityInBytes, blockSizeInBytes, maxCapacityInBytes)
	if err != nil {
		klog.Errorf("failed to round capacity: %v", err)
		return nil, err
	}
	klog.V(2).Infof("capacity (in bytes) after rounding to next cluster increment: %#v", capacityInBytes)

	storageCapacityTib := float32(capacityInBytes) / util.TiB
	klog.V(2).Infof("storage capacity requested (in TiB): %#v", storageCapacityTib)

	// check if capacity is within the limit
	if capacityRange.GetLimitBytes() != 0 && capacityInBytes > capacityRange.GetLimitBytes() {
		return nil, status.Errorf(codes.InvalidArgument,
			"CreateVolume required capacity %v is greater than capacity limit %v",
			capacityInBytes, capacityRange.GetLimitBytes())
	}

	if shouldCreateAmlfsCluster {
		amlFilesystemProperties.StorageCapacityTiB = storageCapacityTib

		if len(availableZones) > 0 {
			klog.V(2).Infof("available zones for SKU %s in location %s: %v", amlFilesystemProperties.SKUName, amlFilesystemProperties.Location, availableZones)
			if len(amlFilesystemProperties.Zone) == 0 {
				return nil, status.Errorf(codes.InvalidArgument,
					"CreateVolume Parameter %s must be provided for dynamically provisioned AMLFS in location %s, available zones: %v",
					VolumeContextZone, amlFilesystemProperties.Location, availableZones)
			}
			if !slices.Contains(availableZones, amlFilesystemProperties.Zone) {
				return nil, status.Errorf(codes.InvalidArgument,
					"CreateVolume Parameter %s %s must be one of: %v",
					VolumeContextZone, amlFilesystemProperties.Zone, availableZones)
			}
		} else {
			klog.Warningf("no zones available for SKU %s in location %s", amlFilesystemProperties.SKUName, amlFilesystemProperties.Location)
			if len(amlFilesystemProperties.Zone) > 0 {
				return nil, status.Errorf(codes.InvalidArgument,
					"CreateVolume Parameter %s cannot be used in location %s, no zones available for SKU %s",
					VolumeContextZone, amlFilesystemProperties.Location, amlFilesystemProperties.SKUName)
			}
		}

		if !isValidVolumeName(volName) {
			return nil, status.Errorf(codes.InvalidArgument,
				"CreateVolume invalid volume name %s, cannot create valid AMLFS name. Check length and characters",
				volName)
		}
		amlFilesystemProperties.AmlFilesystemName = volName

		klog.V(2).Infof(
			"beginning to create AMLFS cluster (%s): %#v", amlFilesystemProperties.AmlFilesystemName,
			amlFilesystemProperties,
		)

		mgsIPAddress, err = d.dynamicProvisioner.CreateAmlFilesystem(ctx, amlFilesystemProperties)
		if err != nil {
			errCode := status.Code(err)
			if errCode == codes.Unknown {
				klog.Errorf("unknown error occurred when creating AMLFS %s: %v", amlFilesystemProperties.AmlFilesystemName, err)
				return nil, status.Error(codes.Unknown, err.Error())
			}
			klog.Errorf("error when creating AMLFS %s: %v", amlFilesystemProperties.AmlFilesystemName, err)
			return nil, status.Errorf(errCode, "CreateVolume error when creating AMLFS %s: %v", amlFilesystemProperties.AmlFilesystemName, err)
		}

		util.SetKeyValueInMap(parameters, VolumeContextResourceGroupName, amlFilesystemProperties.ResourceGroupName)
		util.SetKeyValueInMap(parameters, VolumeContextMGSIPAddress, mgsIPAddress)
		util.SetKeyValueInMap(parameters, VolumeContextFSName, DefaultLustreFsName)
	}

	util.SetKeyValueInMap(parameters, VolumeContextInternalDynamicallyCreated, createdByDynamicProvisioningStringValue)

	volumeID, err := createVolumeIDFromParams(volName, parameters)
	if err != nil {
		return nil, err
	}

	klog.V(2).Infof("created volumeID(%s) successfully", volumeID)

	isOperationSucceeded = true

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacityInBytes,
			VolumeContext: parameters,
		},
	}, nil
}

func (d *Driver) getSkuValuesForLocation(ctx context.Context, skuName, location string) (*LustreSkuValue, error) {
	skus, err := d.dynamicProvisioner.GetSkuValuesForLocation(ctx, location)
	if err != nil {
		return nil, err
	}
	retrievedSkuValue, ok := skus[skuName]
	if !ok {
		validSkuNames := slices.Sorted(maps.Keys(skus))
		return nil, status.Errorf(
			codes.InvalidArgument,
			"CreateVolume Parameter %s must be one of: %v",
			VolumeContextSkuName,
			validSkuNames,
		)
	}
	return retrievedSkuValue, nil
}

func (d *Driver) roundToAmlfsBlockSize(capacityInBytes, blockSizeInBytes, maxCapacityInBytes int64) (int64, error) {
	if capacityInBytes == 0 {
		capacityInBytes = defaultSizeInBytes
	}

	if blockSizeInBytes == 0 {
		blockSizeInBytes = defaultLaaSOBlockSizeInTib * util.TiB
	}

	roundedCapacityInBytes := ((capacityInBytes + blockSizeInBytes - 1) /
		blockSizeInBytes) * blockSizeInBytes

	if roundedCapacityInBytes < capacityInBytes {
		return 0, status.Errorf(codes.InvalidArgument, "Requested capacity %d cannot be rounded up to next block of size %d, value overflow", capacityInBytes, blockSizeInBytes)
	}

	if maxCapacityInBytes > 0 && roundedCapacityInBytes > maxCapacityInBytes {
		return 0, status.Errorf(codes.InvalidArgument, "Requested capacity %d exceeds maximum capacity %d for SKU in this location", capacityInBytes, maxCapacityInBytes)
	}

	return roundedCapacityInBytes, nil
}

func checkVolumeRequest(req *csi.CreateVolumeRequest) error {
	volumeCapabilities := req.GetVolumeCapabilities()
	if len(volumeCapabilities) == 0 {
		return status.Error(
			codes.InvalidArgument,
			"CreateVolume Volume capabilities must be provided",
		)
	}
	if req.GetVolumeContentSource() != nil {
		return status.Error(
			codes.InvalidArgument,
			"CreateVolume doesn't support being created from an existing volume",
		)
	}
	if req.GetSecrets() != nil {
		return status.Error(
			codes.InvalidArgument,
			"CreateVolume doesn't support secrets",
		)
	}
	if req.GetAccessibilityRequirements() != nil {
		return status.Error(
			codes.InvalidArgument,
			"CreateVolume doesn't support accessibility_requirements",
		)
	}
	capabilityError := validateVolumeCapabilities(volumeCapabilities)
	if capabilityError != nil {
		return capabilityError
	}
	return nil
}

// DeleteVolume delete a volume
func (d *Driver) DeleteVolume(
	ctx context.Context, req *csi.DeleteVolumeRequest,
) (*csi.DeleteVolumeResponse, error) {
	mc := metrics.NewMetricContext(azureLustreCSIDriverName,
		"controller_delete_volume",
		d.resourceGroup,
		d.cloud.SubscriptionID,
		d.Name)

	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"Volume ID missing in request")
	}
	if req.GetSecrets() != nil {
		return nil, status.Error(
			codes.InvalidArgument,
			"CreateVolume doesn't support secrets",
		)
	}

	lustreVolume, err := getLustreVolFromID(volumeID)
	if err != nil {
		klog.Warningf("error parsing volume ID '%v'", err)
	}

	if acquired := d.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted,
			volumeOperationAlreadyExistsFmt,
			volumeID)
	}
	defer d.volumeLocks.Release(volumeID)

	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof("deleting volumeID(%s)", volumeID)

	if lustreVolume != nil && lustreVolume.createdByDynamicProvisioning {
		amlFilesystemName := lustreVolume.name
		resourceGroupName := lustreVolume.resourceGroupName

		if resourceGroupName == "" {
			return nil, status.Errorf(codes.InvalidArgument, "volume was dynamically created but associated resource group is not specified. AMLFS cluster may need to be deleted manually")
		}

		err := d.dynamicProvisioner.DeleteAmlFilesystem(ctx, resourceGroupName, amlFilesystemName)
		if err != nil {
			errCode := status.Code(err)
			if errCode == codes.Unknown {
				klog.Errorf("unknown error occurred when deleting AMLFS %s in resource group %s: %v", amlFilesystemName, lustreVolume.resourceGroupName, err)
				return nil, status.Error(codes.Unknown, err.Error())
			}
			klog.Errorf("error when deleting AMLFS %s in resource group %s: %v", amlFilesystemName, lustreVolume.resourceGroupName, err)
			return nil, status.Errorf(errCode, "DeleteVolume error when deleting AMLFS %s in resource group %s: %v", amlFilesystemName, lustreVolume.resourceGroupName, err)
		}
	}

	isOperationSucceeded = true
	klog.V(2).Infof("volumeID(%s) is deleted successfully", volumeID)
	return &csi.DeleteVolumeResponse{}, nil
}

// ValidateVolumeCapabilities return the capabilities of the volume
func (d *Driver) ValidateVolumeCapabilities(
	_ context.Context,
	req *csi.ValidateVolumeCapabilitiesRequest,
) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if req.GetSecrets() != nil {
		return nil, status.Error(
			codes.InvalidArgument,
			"Doesn't support secrets",
		)
	}
	// TODO_CHYIN: need to check if the volumeID is a exist volume
	//             need LaaSo's support
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"Volume ID missing in request")
	}
	capabilities := req.GetVolumeCapabilities()
	if len(capabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"Volume capabilities missing in request")
	}

	confirmed := &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
		VolumeCapabilities: capabilities,
	}
	capabilityError := validateVolumeCapabilities(capabilities)
	if capabilityError != nil {
		confirmed = nil
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: confirmed,
		Message:   "",
	}, nil
}

// ControllerGetCapabilities returns the capabilities of the Controller plugin
func (d *Driver) ControllerGetCapabilities(
	_ context.Context,
	_ *csi.ControllerGetCapabilitiesRequest,
) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: d.Cap,
	}, nil
}

// Convert VolumeCreate parameters to a volume id
func createVolumeIDFromParams(volName string, params map[string]string) (string, error) {
	var mgsIPAddress, createdByDynamicProvisioningStringValue, resourceGroupName, subDir string

	// validate parameters (case-insensitive).
	for k, v := range params {
		switch strings.ToLower(k) {
		case VolumeContextMGSIPAddress:
			mgsIPAddress = v
		case VolumeContextInternalDynamicallyCreated:
			createdByDynamicProvisioningStringValue = v
		case VolumeContextResourceGroupName:
			resourceGroupName = v
		case VolumeContextSubDir:
			subDir = v
			subDir = strings.Trim(subDir, "/")

			if len(subDir) == 0 {
				return "", status.Error(
					codes.InvalidArgument,
					"CreateVolume Parameter sub-dir must not be empty if provided",
				)
			}
		}
	}

	volumeID := fmt.Sprintf(volumeIDTemplate, volName, DefaultLustreFsName, mgsIPAddress, subDir, createdByDynamicProvisioningStringValue, resourceGroupName)

	return volumeID, nil
}
