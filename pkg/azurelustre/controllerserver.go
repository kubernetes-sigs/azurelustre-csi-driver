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
	"errors"
	"fmt"
	"maps"
	"net/netip"
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
	amlFilesystemNameRegex = regexp.MustCompile(fmt.Sprintf(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,%d}[a-zA-Z0-9]$`, amlFilesystemNameMaxLength-2))
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
	RequiredBytes        int64
	LimitBytes           int64
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
			shouldCreateAmlfsCluster = propertyValue == ""
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
					if strings.EqualFold(tag, pvcNameTag) || strings.EqualFold(tag, pvcNamespaceTag) ||
						strings.EqualFold(tag, pvNameTag) || strings.EqualFold(tag, createdByTag) {
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
		if len(amlFilesystemProperties.Tags) > util.MaxAzureResourceTags {
			return nil, status.Errorf(codes.InvalidArgument,
				"CreateVolume permits at most %d Azure tags including driver metadata; received %d",
				util.MaxAzureResourceTags, len(amlFilesystemProperties.Tags))
		}
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

// requireDynamicProvisioner rejects volume lifecycle RPCs on pods that never
// built Azure clients. Though avoided in practice due to sidecar configuration,
// both pods are currently configured as serving the controller service, so a
// misrouted request must be answered rather than dereference a nil provisioner.
func (d *Driver) requireDynamicProvisioner() error {
	if d.dynamicProvisioner == nil {
		return status.Errorf(codes.FailedPrecondition,
			"volume lifecycle requests are served by the controller, not this %s pod", d.podRole)
	}
	return nil
}

// CreateVolume provisions a volume
func (d *Driver) CreateVolume(
	ctx context.Context,
	req *csi.CreateVolumeRequest,
) (*csi.CreateVolumeResponse, error) {
	if err := d.requireDynamicProvisioner(); err != nil {
		return nil, err
	}
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

	parameters := maps.Clone(req.GetParameters())
	if parameters == nil {
		return nil, status.Error(codes.InvalidArgument,
			"CreateVolume Parameters must be provided")
	}
	if _, err := createVolumeIDFromParams(volName, parameters); err != nil {
		return nil, err
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

	lockKey := volName
	if shouldCreateAmlfsCluster {
		if len(amlFilesystemProperties.ResourceGroupName) == 0 {
			amlFilesystemProperties.ResourceGroupName = d.resourceGroup
		}
		if err := validateVolumeIDField(VolumeContextResourceGroupName, amlFilesystemProperties.ResourceGroupName); err != nil {
			return nil, err
		}
		lockKey = amlFilesystemLockKey(d.cloud.SubscriptionID, amlFilesystemProperties.ResourceGroupName, volName)
	}
	if acquired := d.volumeLocks.TryAcquire(lockKey); !acquired {
		return nil, status.Errorf(codes.Aborted,
			volumeOperationAlreadyExistsFmt,
			volName)
	}
	defer d.volumeLocks.Release(lockKey)

	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	capacityRange := req.GetCapacityRange()
	if capacityRange.GetRequiredBytes() < 0 || capacityRange.GetLimitBytes() < 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume capacity must not be negative")
	}
	if capacityRange.GetLimitBytes() > 0 && capacityRange.GetRequiredBytes() > capacityRange.GetLimitBytes() {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume required capacity is greater than capacity limit")
	}

	capacityInBytes := capacityRange.GetRequiredBytes()
	if capacityInBytes == 0 {
		capacityInBytes = defaultSizeInBytes
		klog.V(2).Infof("using default capacity: %#v", capacityInBytes)
	}

	blockSizeInBytes := int64(defaultLaaSOBlockSizeInTib) * util.TiB
	maxCapacityInBytes := int64(0)

	createdByDynamicProvisioningStringValue := "f"

	availableZones := []string{}
	var lustreSkuValue *LustreSkuValue
	var filesystem *AmlFilesystemResult
	var failedCreate bool

	if shouldCreateAmlfsCluster {
		createdByDynamicProvisioningStringValue = "t"
		if !isValidVolumeName(volName) {
			return nil, status.Errorf(codes.InvalidArgument,
				"CreateVolume invalid volume name %s, cannot create valid AMLFS name. Check length and characters", volName)
		}
		amlFilesystemProperties.AmlFilesystemName = volName
		amlFilesystemProperties.RequiredBytes = capacityRange.GetRequiredBytes()
		amlFilesystemProperties.LimitBytes = capacityRange.GetLimitBytes()

		if len(amlFilesystemProperties.Location) == 0 {
			amlFilesystemProperties.Location = d.location
		}

		amlFilesystemProperties.SubnetInfo = d.populateSubnetPropertiesFromCloudConfig(amlFilesystemProperties.SubnetInfo)

		filesystem, err = d.dynamicProvisioner.GetAmlFilesystem(ctx, amlFilesystemProperties)
		failedCreate = errors.Is(err, errAmlFilesystemFailed)
		if err != nil && !failedCreate {
			return nil, err
		}
		if filesystem == nil {
			klog.V(2).Infof("finding capacity based on SKU %s for location %s", amlFilesystemProperties.SKUName, amlFilesystemProperties.Location)
			lustreSkuValue, err = d.getSkuValuesForLocation(ctx, amlFilesystemProperties.SKUName, amlFilesystemProperties.Location)
			if err != nil {
				return nil, err
			}
			if lustreSkuValue != nil {
				blockSizeInBytes = lustreSkuValue.IncrementInTib * util.TiB
				maxCapacityInBytes = lustreSkuValue.MaximumInTib * util.TiB
				availableZones = lustreSkuValue.AvailableZones
			}
		} else {
			capacityInBytes = filesystem.CapacityBytes
		}
	}

	if filesystem == nil {
		capacityInBytes, err = d.roundToAmlfsBlockSize(capacityInBytes, blockSizeInBytes, maxCapacityInBytes)
		if err != nil {
			klog.Errorf("failed to round capacity: %v", err)
			return nil, err
		}
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

	if shouldCreateAmlfsCluster && filesystem == nil {
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
		} else if lustreSkuValue != nil {
			klog.Warningf("no zones available for SKU %s in location %s", amlFilesystemProperties.SKUName, amlFilesystemProperties.Location)
			if len(amlFilesystemProperties.Zone) > 0 {
				if !d.allowUnadvertisedZones {
					return nil, status.Errorf(codes.InvalidArgument,
						"CreateVolume Parameter %s cannot be used in location %s, no zones available for SKU %s",
						VolumeContextZone, amlFilesystemProperties.Location, amlFilesystemProperties.SKUName)
				}
				klog.Warningf(
					"allowing explicitly supplied zone %s for SKU %s in location %s because --allow-unadvertised-zones is enabled",
					amlFilesystemProperties.Zone,
					amlFilesystemProperties.SKUName,
					amlFilesystemProperties.Location,
				)
			}
		}

		if !failedCreate {
			if err := d.dynamicProvisioner.ValidateSubnetCapacity(ctx, amlFilesystemProperties); err != nil {
				return nil, err
			}
		}
	}

	if shouldCreateAmlfsCluster {
		if filesystem == nil {
			klog.V(2).Infof("beginning to create AMLFS cluster (%s)", volName)
			filesystem, err = d.dynamicProvisioner.CreateAmlFilesystem(ctx, amlFilesystemProperties)
			if err != nil {
				klog.Errorf("error when creating AMLFS %s: %v", volName, err)
				return nil, status.Errorf(status.Code(err), "CreateVolume error when creating AMLFS %s: %v", volName, err)
			}
		}
		if filesystem == nil {
			return nil, status.Error(codes.Unavailable, "AMLFS returned no filesystem")
		}
		address, addressErr := netip.ParseAddr(filesystem.MGSAddress)
		if addressErr != nil || !address.Is4() || filesystem.CapacityBytes <= 0 ||
			filesystem.CapacityBytes < capacityRange.GetRequiredBytes() ||
			(capacityRange.GetLimitBytes() > 0 && filesystem.CapacityBytes > capacityRange.GetLimitBytes()) {
			return nil, status.Error(codes.Unavailable, "AMLFS returned invalid MGS address or capacity")
		}
		mgsIPAddress = address.String()
		capacityInBytes = filesystem.CapacityBytes
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
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if value, ok := skus[skuName]; ok {
		if err != nil {
			klog.V(4).Infof("using validated metadata for SKU %s in location %s despite incomplete lookup: %v", skuName, location, err)
		}
		return value, nil
	}
	if err != nil {
		klog.Warningf("cannot retrieve SKU metadata for %s in location %s: %v; continuing creation with %d TiB capacity rounding and deferring SKU, zone and capacity validation to ARM",
			skuName, location, err, defaultLaaSOBlockSizeInTib)
		return nil, nil
	}
	validSkuNames := slices.Sorted(maps.Keys(skus))
	return nil, status.Errorf(
		codes.InvalidArgument,
		"CreateVolume Parameter %s must be one of: %v",
		VolumeContextSkuName,
		validSkuNames,
	)
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
	if err := d.requireDynamicProvisioner(); err != nil {
		return nil, err
	}
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

	lockKey := volumeID
	if lustreVolume != nil && lustreVolume.createdByDynamicProvisioning {
		if lustreVolume.resourceGroupName == "" {
			return nil, status.Errorf(codes.InvalidArgument, "volume was dynamically created but associated resource group is not specified. AMLFS cluster may need to be deleted manually")
		}
		lockKey = amlFilesystemLockKey(d.cloud.SubscriptionID, lustreVolume.resourceGroupName, lustreVolume.name)
	}
	if acquired := d.volumeLocks.TryAcquire(lockKey); !acquired {
		return nil, status.Errorf(codes.Aborted,
			volumeOperationAlreadyExistsFmt,
			volumeID)
	}
	defer d.volumeLocks.Release(lockKey)

	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof("deleting volumeID(%s)", volumeID)

	if lustreVolume != nil && lustreVolume.createdByDynamicProvisioning {
		amlFilesystemName := lustreVolume.name
		resourceGroupName := lustreVolume.resourceGroupName

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

func validateVolumeIDField(name, value string) error {
	if strings.ContainsAny(value, separator+"\x00") {
		return status.Errorf(codes.InvalidArgument, "CreateVolume %s contains a reserved volume ID character", name)
	}
	return nil
}

// Convert VolumeCreate parameters to a volume id
func createVolumeIDFromParams(volName string, params map[string]string) (string, error) {
	if err := validateVolumeIDField("name", volName); err != nil {
		return "", err
	}
	var mgsIPAddress, createdByDynamicProvisioningStringValue, resourceGroupName, subDir string

	// validate parameters (case-insensitive).
	for k, v := range params {
		key := strings.ToLower(k)
		if key == VolumeContextMGSIPAddress || key == VolumeContextResourceGroupName {
			if err := validateVolumeIDField(key, v); err != nil {
				return "", err
			}
		}
		switch key {
		case VolumeContextMGSIPAddress:
			mgsIPAddress = v
		case VolumeContextInternalDynamicallyCreated:
			createdByDynamicProvisioningStringValue = v
		case VolumeContextResourceGroupName:
			resourceGroupName = v
		case VolumeContextSubDir:
			subDir = strings.Trim(v, "/")
			if len(subDir) == 0 {
				return "", status.Error(
					codes.InvalidArgument,
					"CreateVolume Parameter sub-dir must not be empty if provided",
				)
			}
			if strings.Contains(subDir, separator) {
				return "", status.Error(codes.InvalidArgument, "CreateVolume Parameter sub-dir must not contain '#'")
			}
		}
	}

	volumeID := fmt.Sprintf(volumeIDTemplate, volName, DefaultLustreFsName, mgsIPAddress, subDir, createdByDynamicProvisioningStringValue, resourceGroupName)

	return volumeID, nil
}
