/*
Copyright 2024 The Kubernetes Authors.

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
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/util"
)

const (
	AmlfsSkuResourceType          = "amlFilesystems"
	AmlfsSkuCapacityIncrementName = "OSS capacity increment (TiB)"
	AmlfsSkuCapacityMaximumName   = "default maximum capacity (TiB)"
	authFailureHintFormat         = "error occurred calling API: %v; verify the identity has the required RBAC role assignments (for workload identity, confirm its federated credential is configured)"
)

type AmlFilesystemResult struct {
	MGSAddress    string
	CapacityBytes int64
}

type DynamicProvisionerInterface interface {
	ValidateSubnetCapacity(ctx context.Context, properties *AmlFilesystemProperties) error
	DeleteAmlFilesystem(ctx context.Context, resourceGroupName, amlFilesystemName string) error
	CreateAmlFilesystem(ctx context.Context, amlFilesystemProperties *AmlFilesystemProperties) (*AmlFilesystemResult, error)
	GetAmlFilesystem(ctx context.Context, amlFilesystemProperties *AmlFilesystemProperties) (*AmlFilesystemResult, error)
	GetSkuValuesForLocation(ctx context.Context, location string) (map[string]*LustreSkuValue, error)
}

type DynamicProvisioner struct {
	DynamicProvisionerInterface
	subscriptionID       string
	amlFilesystemsClient *armstoragecache.AmlFilesystemsClient
	mgmtClient           *armstoragecache.ManagementClient
	skusClient           *armstoragecache.SKUsClient
	vnetClient           *armnetwork.VirtualNetworksClient
	pollFrequency        time.Duration
}

func amlFilesystemResourceID(subscriptionID, resourceGroupName, amlFilesystemName string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.StorageCache/amlFilesystems/%s",
		subscriptionID, resourceGroupName, amlFilesystemName)
}

func convertHTTPResponseErrorToGrpcCodeError(err error) error {
	if err == nil {
		return nil
	}

	_, ok := status.FromError(err)
	if ok {
		// The error is already a gRPC status error
		return err
	}

	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, err.Error())
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, err.Error())
	}

	var httpError *azcore.ResponseError
	if !errors.As(err, &httpError) {
		klog.Errorf("error is not a response error: %v", err)
		return status.Errorf(codes.Unknown, "error occurred calling API: %v", err)
	}

	statusCode := httpError.StatusCode

	grpcErrorCode := codes.Unknown
	switch {
	case statusCode >= 400 && statusCode < 500:
		switch statusCode {
		case http.StatusBadRequest:
			grpcErrorCode = codes.InvalidArgument
		case http.StatusRequestTimeout:
			grpcErrorCode = codes.DeadlineExceeded
		case http.StatusConflict:
			if strings.Contains(err.Error(), "Operation results in exceeding quota limits of resource type AmlFilesystem") {
				grpcErrorCode = codes.ResourceExhausted
			} else {
				grpcErrorCode = codes.InvalidArgument
			}
		case http.StatusNotFound:
			grpcErrorCode = codes.NotFound
		case http.StatusForbidden:
			return status.Errorf(codes.PermissionDenied, authFailureHintFormat, httpError)
		case http.StatusUnauthorized:
			return status.Errorf(codes.Unauthenticated, authFailureHintFormat, httpError)
		case http.StatusTooManyRequests:
			grpcErrorCode = codes.Unavailable
		default:
			grpcErrorCode = codes.InvalidArgument
		}
	case statusCode >= 500:
		switch statusCode {
		case http.StatusInternalServerError:
			grpcErrorCode = codes.Internal
		case http.StatusBadGateway:
			grpcErrorCode = codes.Unavailable
		case http.StatusServiceUnavailable:
			grpcErrorCode = codes.Unavailable
		case http.StatusGatewayTimeout:
			grpcErrorCode = codes.DeadlineExceeded
		default:
			// Unknown preserves an unmapped status; it is not a retryability guarantee.
			grpcErrorCode = codes.Unknown
		}
	case strings.Contains(httpError.ErrorCode, "Error") ||
		strings.Contains(httpError.ErrorCode, "Timeout") ||
		strings.Contains(httpError.ErrorCode, "Fail"):
		// Special case for 200 status errors to ensure preserve the reason
		return status.Errorf(codes.DeadlineExceeded, "%s: %v", httpError.ErrorCode, httpError)
	}

	return status.Errorf(grpcErrorCode, "error occurred calling API: %v", httpError)
}

// Use only when observation failed or accepted work still needs recovery, not for
// request validation, incompatible resources, or a rejected fresh submission.
func unresolvedAmlFilesystemError(err error) error {
	err = convertHTTPResponseErrorToGrpcCodeError(err)
	code := status.Code(err)
	if code == codes.Aborted || code == codes.Canceled || code == codes.DeadlineExceeded || code == codes.Unavailable {
		return err
	}
	return status.Errorf(codes.Unavailable, "AMLFS operation outcome is unresolved: %v", err)
}

func (d *DynamicProvisioner) readAmlFilesystem(ctx context.Context, resourceGroupName, amlFilesystemName string) (*armstoragecache.AmlFilesystem, error) {
	if d.amlFilesystemsClient == nil {
		return nil, status.Error(codes.Internal, "aml filesystem client is nil")
	}
	if d.subscriptionID == "" {
		return nil, status.Error(codes.Internal, "AMLFS subscription ID is not configured")
	}
	if resourceGroupName == "" || amlFilesystemName == "" {
		return nil, status.Error(codes.InvalidArgument, "AMLFS resource group and filesystem name must be provided")
	}

	resourceID := amlFilesystemResourceID(d.subscriptionID, resourceGroupName, amlFilesystemName)
	resp, err := d.amlFilesystemsClient.Get(ctx, resourceGroupName, amlFilesystemName, nil)
	if err != nil {
		var responseError *azcore.ResponseError
		if errors.As(err, &responseError) && responseError.StatusCode == http.StatusNotFound && responseError.ErrorCode == "ResourceNotFound" {
			return nil, nil
		}

		st := status.Convert(unresolvedAmlFilesystemError(err))
		return nil, status.Errorf(st.Code(), "could not observe AMLFS %s: %s", resourceID, st.Message())
	}

	if err := validateAmlFilesystemIdentity(&resp.AmlFilesystem, resourceID); err != nil {
		return nil, err
	}
	return &resp.AmlFilesystem, nil
}

// GetAmlFilesystem returns nil, nil only after ARM verifies ResourceNotFound.
func (d *DynamicProvisioner) GetAmlFilesystem(ctx context.Context, properties *AmlFilesystemProperties) (*AmlFilesystemResult, error) {
	if properties == nil {
		return nil, status.Error(codes.InvalidArgument, "AMLFS properties must be provided")
	}
	if properties.RequiredBytes < 0 || properties.LimitBytes < 0 ||
		(properties.LimitBytes != 0 && properties.RequiredBytes > properties.LimitBytes) {
		return nil, status.Error(codes.InvalidArgument, "invalid AMLFS capacity range")
	}
	filesystem, err := d.readAmlFilesystem(ctx, properties.ResourceGroupName, properties.AmlFilesystemName)
	if err != nil || filesystem == nil {
		return nil, err
	}
	return d.amlFilesystemResult(filesystem, properties)
}

func (d *DynamicProvisioner) DeleteAmlFilesystem(ctx context.Context, resourceGroupName, amlFilesystemName string) error {
	if d.amlFilesystemsClient == nil {
		return status.Error(codes.Internal, "aml filesystem client is nil")
	}

	poller, err := d.amlFilesystemsClient.BeginDelete(ctx, resourceGroupName, amlFilesystemName, nil)
	if err != nil {
		klog.Warningf("failed to finish the request: %v", err)
		return convertHTTPResponseErrorToGrpcCodeError(err)
	}

	pollerOptions := &runtime.PollUntilDoneOptions{
		Frequency: d.pollFrequency,
	}
	_, err = poller.PollUntilDone(ctx, pollerOptions)
	if err != nil {
		klog.Warningf("failed to poll the result: %v", err)
		return convertHTTPResponseErrorToGrpcCodeError(err)
	}

	klog.V(2).Infof("Successfully deleted AML filesystem: %s", amlFilesystemName)
	return nil
}

func (d *DynamicProvisioner) CreateAmlFilesystem(ctx context.Context, amlFilesystemProperties *AmlFilesystemProperties) (*AmlFilesystemResult, error) {
	result, err := d.GetAmlFilesystem(ctx, amlFilesystemProperties)
	if errors.Is(err, errAmlFilesystemFailed) {
		return nil, d.tryDeleteBeforeRetry(ctx, amlFilesystemProperties)
	}
	if err != nil || result != nil {
		return result, err
	}

	// Only verified absence permits a PUT. Reuse is never an update operation.
	if err := d.ValidateSubnetCapacity(ctx, amlFilesystemProperties); err != nil {
		return nil, err
	}

	tags := make(map[string]*string, len(amlFilesystemProperties.Tags))
	for key, value := range amlFilesystemProperties.Tags {
		tags[key] = new(value)
	}
	properties := &armstoragecache.AmlFilesystemProperties{
		FilesystemSubnet: new(amlFilesystemProperties.SubnetInfo.SubnetID),
		MaintenanceWindow: &armstoragecache.AmlFilesystemPropertiesMaintenanceWindow{
			DayOfWeek:    new(amlFilesystemProperties.MaintenanceDayOfWeek),
			TimeOfDayUTC: new(amlFilesystemProperties.TimeOfDayUTC),
		},
		StorageCapacityTiB: new(amlFilesystemProperties.StorageCapacityTiB),
	}
	amlFilesystem := armstoragecache.AmlFilesystem{
		Location:   new(amlFilesystemProperties.Location),
		Tags:       tags,
		Properties: properties,
		SKU:        &armstoragecache.SKUName{Name: new(amlFilesystemProperties.SKUName)},
	}
	if amlFilesystemProperties.Zone != "" {
		amlFilesystem.Zones = []*string{new(amlFilesystemProperties.Zone)}
	}
	if amlFilesystemProperties.Identities != nil {
		userAssignedIdentities := make(map[string]*armstoragecache.UserAssignedIdentitiesValue, len(amlFilesystemProperties.Identities))
		for _, identity := range amlFilesystemProperties.Identities {
			userAssignedIdentities[identity] = &armstoragecache.UserAssignedIdentitiesValue{}
		}
		amlFilesystem.Identity = &armstoragecache.AmlFilesystemIdentity{
			Type:                   new(armstoragecache.AmlFilesystemIdentityTypeUserAssigned),
			UserAssignedIdentities: userAssignedIdentities,
		}
	}

	klog.V(2).Infof("creating AMLFS cluster: %#v", amlFilesystemProperties)
	// A lost PUT response must return to the CSI GET-before-create path, not be
	// retried blindly against a resource that Azure may already have created.
	createContext := policy.WithRetryOptions(ctx, policy.RetryOptions{MaxRetries: -1})
	poller, err := d.amlFilesystemsClient.BeginCreateOrUpdate(
		createContext,
		amlFilesystemProperties.ResourceGroupName,
		amlFilesystemProperties.AmlFilesystemName,
		amlFilesystem,
		nil,
	)
	if err != nil {
		return nil, d.amlFilesystemCreateError(amlFilesystemProperties, err)
	}

	pollerOptions := &runtime.PollUntilDoneOptions{
		Frequency: d.pollFrequency,
	}
	res, err := poller.PollUntilDone(ctx, pollerOptions)
	if err != nil {
		return nil, d.amlFilesystemCreateError(amlFilesystemProperties, unresolvedAmlFilesystemError(err))
	}

	result, err = d.amlFilesystemResult(&res.AmlFilesystem, amlFilesystemProperties)
	if err != nil {
		return nil, err
	}
	klog.V(2).Infof("Successfully created AML filesystem: %s", amlFilesystemProperties.AmlFilesystemName)
	return result, nil
}

func (d *DynamicProvisioner) amlFilesystemCreateError(properties *AmlFilesystemProperties, err error) error {
	st := status.Convert(convertHTTPResponseErrorToGrpcCodeError(err))
	return status.Errorf(st.Code(), "AMLFS %s create did not complete: %s; preserving any accepted Azure work for observation on retry",
		amlFilesystemResourceID(d.subscriptionID, properties.ResourceGroupName, properties.AmlFilesystemName), st.Message())
}

// ValidateSubnetCapacity checks subnet inputs and available IP addresses.
// Creation repeats the check because available capacity can change.
func (d *DynamicProvisioner) ValidateSubnetCapacity(ctx context.Context, properties *AmlFilesystemProperties) error {
	if properties == nil || properties.SubnetInfo.SubnetID == "" || properties.SubnetInfo.SubnetName == "" ||
		properties.SubnetInfo.VnetName == "" || properties.SubnetInfo.VnetResourceGroup == "" {
		return status.Error(codes.InvalidArgument, "invalid subnet info, must have valid subnet ID, subnet name, vnet name, and vnet resource group")
	}
	hasCapacity, err := d.CheckSubnetCapacity(ctx, properties.SubnetInfo, properties.SKUName, properties.StorageCapacityTiB)
	if err != nil {
		return convertHTTPResponseErrorToGrpcCodeError(err)
	}
	if !hasCapacity {
		return status.Errorf(codes.ResourceExhausted, "cannot create AMLFS cluster %s in subnet %s, not enough IP addresses available",
			properties.AmlFilesystemName, properties.SubnetInfo.SubnetID)
	}
	return nil
}

func (d *DynamicProvisioner) tryDeleteBeforeRetry(ctx context.Context, amlFilesystemProperties *AmlFilesystemProperties) error {
	resourceGroupName := amlFilesystemProperties.ResourceGroupName
	amlFilesystemName := amlFilesystemProperties.AmlFilesystemName
	err := d.DeleteAmlFilesystem(ctx, resourceGroupName, amlFilesystemName)
	if err != nil {
		klog.Errorf("error attempting to delete AMLFS cluster %s for creation retry: %v", amlFilesystemProperties.AmlFilesystemName, err)
		st := status.Convert(unresolvedAmlFilesystemError(err))
		return status.Errorf(st.Code(), "AMLFS %s Failed provisioning recovery did not finish: %s",
			amlFilesystemResourceID(d.subscriptionID, resourceGroupName, amlFilesystemName), st.Message())
	}
	return status.Errorf(codes.Aborted, "AMLFS %s unfinished provisioning reported Failed; deletion completed, retry creation",
		amlFilesystemResourceID(d.subscriptionID, resourceGroupName, amlFilesystemName))
}

// GetSkuValuesForLocation returns usable entries even when an error indicates
// that the rest of the catalog is incomplete.
func (d *DynamicProvisioner) GetSkuValuesForLocation(ctx context.Context, location string) (map[string]*LustreSkuValue, error) {
	if d.skusClient == nil {
		klog.Error("skus client is nil")
		return nil, status.Error(codes.Internal, "skus client is nil")
	}

	skusPager := d.skusClient.NewListPager(nil)
	skuValues := make(map[string]*LustreSkuValue)
	var lookupErrors []error

	for skusPager.More() {
		page, err := skusPager.NextPage(ctx)
		if err != nil {
			st := status.Convert(convertHTTPResponseErrorToGrpcCodeError(err))
			lookupErrors = append(lookupErrors, status.Errorf(st.Code(), "error retrieving SKUs for location %s: %s", location, st.Message()))
			break
		}

		for _, sku := range page.Value {
			if sku == nil || sku.ResourceType == nil || *sku.ResourceType == "" {
				lookupErrors = append(lookupErrors, status.Errorf(codes.Unavailable, "received incomplete SKU response for location %s: missing resource type", location))
				continue
			}
			if *sku.ResourceType != AmlfsSkuResourceType {
				continue
			}

			matchesLocation := false
			missingLocation := len(sku.Locations) == 0
			for _, skuLocation := range sku.Locations {
				if skuLocation == nil || *skuLocation == "" {
					missingLocation = true
				} else if strings.EqualFold(*skuLocation, location) {
					matchesLocation = true
					break
				}
			}
			if !matchesLocation {
				if missingLocation {
					lookupErrors = append(lookupErrors, status.Errorf(codes.Unavailable, "received incomplete SKU response for location %s: missing location", location))
				}
				continue
			}

			value, err := parseSkuResponse(sku, location)
			if err != nil {
				lookupErrors = append(lookupErrors, err)
				continue
			}
			skuValues[*sku.Name] = value
		}
	}

	if len(skuValues) == 0 {
		skuValues = nil
		if len(lookupErrors) == 0 {
			lookupErrors = append(lookupErrors, status.Errorf(codes.Internal, "found no AMLFS SKUs for location %s", location))
		}
	}
	return skuValues, errors.Join(lookupErrors...)
}

func parseSkuResponse(sku *armstoragecache.ResourceSKU, location string) (*LustreSkuValue, error) {
	if sku.Name == nil || *sku.Name == "" {
		return nil, status.Errorf(codes.Unavailable, "received incomplete SKU response for location %s: missing SKU name", location)
	}
	availableZones := []string{}
	foundLocation := false
	for _, locationInfo := range sku.LocationInfo {
		if locationInfo == nil || locationInfo.Location == nil || !strings.EqualFold(*locationInfo.Location, location) || locationInfo.Zones == nil {
			continue
		}
		foundLocation = true
		for _, zone := range locationInfo.Zones {
			if zone == nil || *zone == "" {
				return nil, status.Errorf(codes.Unavailable, "received incomplete SKU response for SKU %s in location %s: missing zone", *sku.Name, location)
			}
			availableZones = append(availableZones, *zone)
		}
	}
	if !foundLocation {
		return nil, status.Errorf(codes.Unavailable, "received incomplete SKU response for SKU %s in location %s: missing location info", *sku.Name, location)
	}

	var incrementInTib, maximumInTib int64
	for _, capability := range sku.Capabilities {
		if capability == nil || capability.Name == nil ||
			(*capability.Name != AmlfsSkuCapacityIncrementName && *capability.Name != AmlfsSkuCapacityMaximumName) {
			continue
		}
		if capability.Value == nil || *capability.Value == "" {
			return nil, status.Errorf(codes.Unavailable, "received incomplete SKU response for SKU %s in location %s: missing value for capability %s", *sku.Name, location, *capability.Name)
		}
		parsedValue, err := strconv.ParseInt(*capability.Value, 10, 64)
		if err != nil {
			return nil, status.Errorf(codes.Unavailable,
				"received invalid SKU response for SKU %s in location %s: invalid %s value %q: %v",
				*sku.Name, location, *capability.Name, *capability.Value, err)
		}
		if parsedValue <= 0 || parsedValue > math.MaxInt64/util.TiB {
			return nil, status.Errorf(codes.Unavailable,
				"received invalid SKU response for SKU %s in location %s: %s must be positive and fit in CSI capacity bytes, received %q",
				*sku.Name, location, *capability.Name, *capability.Value)
		}
		if *capability.Name == AmlfsSkuCapacityIncrementName {
			incrementInTib = parsedValue
		} else {
			maximumInTib = parsedValue
		}
	}
	if incrementInTib == 0 || maximumInTib == 0 || incrementInTib > maximumInTib {
		return nil, status.Errorf(codes.Unavailable,
			"received incomplete or inconsistent SKU response for SKU %s in location %s: missing or inconsistent capacity increment or maximum",
			*sku.Name, location)
	}
	sort.Strings(availableZones)
	return &LustreSkuValue{
		IncrementInTib: incrementInTib,
		MaximumInTib:   maximumInTib,
		AvailableZones: availableZones,
	}, nil
}

func (d *DynamicProvisioner) getAmlfsSubnetSize(ctx context.Context, sku string, clusterSize float32) (int, error) {
	if d.mgmtClient == nil {
		return 0, status.Error(codes.Internal, "storage management client is nil")
	}

	reqSize, err := d.mgmtClient.GetRequiredAmlFSSubnetsSize(ctx, &armstoragecache.ManagementClientGetRequiredAmlFSSubnetsSizeOptions{
		RequiredAMLFilesystemSubnetsSizeInfo: &armstoragecache.RequiredAmlFilesystemSubnetsSizeInfo{
			SKU: &armstoragecache.SKUName{
				Name: new(sku),
			},
			StorageCapacityTiB: new(clusterSize),
		},
	})
	if err != nil {
		return 0, convertHTTPResponseErrorToGrpcCodeError(err)
	}
	if reqSize.FilesystemSubnetSize == nil {
		klog.Errorf("received nil FilesystemSubnetSize from GetRequiredAmlFSSubnetsSize for SKU: %s, cluster size: %f", sku, clusterSize)
		return 0, status.Error(codes.Internal, "received nil FilesystemSubnetSize from storage management client")
	}
	return int(*reqSize.FilesystemSubnetSize), nil
}

func (d *DynamicProvisioner) checkSubnetAddresses(ctx context.Context, vnetResourceGroup, vnetName, subnetID string) (int, error) {
	if d.vnetClient == nil {
		return 0, status.Error(codes.Internal, "vnet client is nil")
	}
	usagesPager := d.vnetClient.NewListUsagePager(vnetResourceGroup, vnetName, nil)

	for usagesPager.More() {
		page, err := usagesPager.NextPage(ctx)
		if err != nil {
			return 0, convertHTTPResponseErrorToGrpcCodeError(err)
		}

		for _, usageValue := range page.Value {
			if usageValue == nil || usageValue.ID == nil || *usageValue.ID == "" {
				return 0, status.Errorf(codes.Unavailable,
					"received incomplete subnet usage response for vnet %s in resource group %s: missing subnet ID",
					vnetName, vnetResourceGroup)
			}
			// Azure resource group names are case-insensitive, but different ARM
			// endpoints may echo back different casing for the same resource ID,
			// so compare case-insensitively to avoid false "subnet not found" errors.
			if strings.EqualFold(*usageValue.ID, subnetID) {
				if usageValue.CurrentValue == nil || usageValue.Limit == nil {
					return 0, status.Errorf(codes.Unavailable,
						"received incomplete subnet usage response for subnet %s in vnet %s, resource group %s: missing CurrentValue or Limit",
						subnetID, vnetName, vnetResourceGroup)
				}
				usedIPs := *usageValue.CurrentValue
				limitIPs := *usageValue.Limit
				availableIPs := int(limitIPs) - int(usedIPs)
				return availableIPs, nil
			}
		}
	}
	klog.Warningf("subnet %s not found in vnet %s, resource group %s. Ensure permissions are correct for configuration.", subnetID, vnetName, vnetResourceGroup)
	return 0, status.Errorf(codes.FailedPrecondition, "subnet %s not found in vnet %s, resource group %s. Ensure permissions are correct for configuration.", subnetID, vnetName, vnetResourceGroup)
}

func (d *DynamicProvisioner) CheckSubnetCapacity(ctx context.Context, subnetInfo SubnetProperties, sku string, clusterSize float32) (bool, error) {
	requiredSubnetIPSize, err := d.getAmlfsSubnetSize(ctx, sku, clusterSize)
	var availableIPs int
	if err == nil {
		availableIPs, err = d.checkSubnetAddresses(ctx, subnetInfo.VnetResourceGroup, subnetInfo.VnetName, subnetInfo.SubnetID)
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, status.FromContextError(ctx.Err()).Err()
		}
		// ARM also validates subnet capacity during creation.
		if status.Code(err) == codes.PermissionDenied {
			klog.Warningf("cannot check subnet capacity for %s: %v; continuing creation and deferring subnet capacity validation to ARM", subnetInfo.SubnetID, err)
			return true, nil
		}
		klog.Errorf("error checking subnet capacity for %s: %v", subnetInfo.SubnetID, err)
		return false, convertHTTPResponseErrorToGrpcCodeError(err)
	}

	if requiredSubnetIPSize > availableIPs {
		klog.Warningf("There is not enough room in the %s subnet to fit a %s SKU cluster: %v needed, %v available", subnetInfo.SubnetID, sku, requiredSubnetIPSize, availableIPs)
		return false, nil
	}
	klog.V(2).Infof("There is enough room in the %s subnet to fit a %s SKU cluster: %v needed, %v available", subnetInfo.SubnetID, sku, requiredSubnetIPSize, availableIPs)
	return true, nil
}
