package azurelustre

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

type ClusterState string

const (
	ClusterStateExists            ClusterState = "Exists"
	ClusterStateNotFound          ClusterState = "Not found"
	ClusterStateDeleting          ClusterState = "Deleting"
	ClusterStateFailed            ClusterState = "Failed"
	AmlfsSkuResourceType                       = "amlFilesystems"
	AmlfsSkuCapacityIncrementName              = "OSS capacity increment (TiB)"
	AmlfsSkuCapacityMaximumName                = "default maximum capacity (TiB)"
)

type DynamicProvisionerInterface interface {
	DeleteAmlFilesystem(ctx context.Context, resourceGroupName, amlFilesystemName string) error
	CreateAmlFilesystem(ctx context.Context, amlFilesystemProperties *AmlFilesystemProperties) (string, error)
	GetSkuValuesForLocation(ctx context.Context, location string) map[string]*LustreSkuValue
}

type DynamicProvisioner struct {
	DynamicProvisionerInterface
	amlFilesystemsClient *armstoragecache.AmlFilesystemsClient
	mgmtClient           *armstoragecache.ManagementClient
	skusClient           *armstoragecache.SKUsClient
	vnetClient           *armnetwork.VirtualNetworksClient
	defaultSkuValues     map[string]*LustreSkuValue
	pollFrequency        time.Duration
}

func convertHTTPResponseErrorToGrpcCodeError(err error) error {
	if err == nil {
		return nil
	}

	_, ok := status.FromError(err)
	if ok {
		return err
	}

	var httpError *azcore.ResponseError
	if !errors.As(err, &httpError) {
		klog.Errorf("error is not a response error: %v", err)
		return status.Errorf(codes.Unknown, "error occurred calling API: %v", err)
	}

	statusCode := httpError.StatusCode

	grpcErrorCode := codes.Unknown
	if statusCode >= 400 && statusCode < 500 {
		switch statusCode {
		case http.StatusBadRequest:
			grpcErrorCode = codes.InvalidArgument
		case http.StatusConflict:
			if strings.Contains(err.Error(), "Operation results in exceeding quota limits of resource type AmlFilesystem") {
				grpcErrorCode = codes.ResourceExhausted
			} else {
				grpcErrorCode = codes.InvalidArgument
			}
		case http.StatusNotFound:
			grpcErrorCode = codes.NotFound
		case http.StatusForbidden:
			grpcErrorCode = codes.PermissionDenied
		case http.StatusUnauthorized:
			grpcErrorCode = codes.Unauthenticated
		case http.StatusTooManyRequests:
			grpcErrorCode = codes.Unavailable
		default:
			grpcErrorCode = codes.InvalidArgument
		}
	} else if statusCode >= 500 {
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
			// Prefer to default to Unknown rather than Internal so provisioner will retry
			grpcErrorCode = codes.Unknown
		}
	} else if httpError.ErrorCode == "InternalExecutionError" || httpError.ErrorCode == "CreateTimeout" {
		// Special case for CreateTimeout to ensure preserve the reason
		return status.Errorf(codes.DeadlineExceeded, "%s: %v", httpError.ErrorCode, httpError)
	}

	return status.Errorf(grpcErrorCode, "error occurred calling API: %v", httpError)
}

func (d *DynamicProvisioner) currentClusterState(ctx context.Context, resourceGroupName, amlFilesystemName string) (ClusterState, error) {
	if d.amlFilesystemsClient == nil {
		return "", status.Error(codes.Internal, "aml filesystem client is nil")
	}

	resp, err := d.amlFilesystemsClient.Get(ctx, resourceGroupName, amlFilesystemName, nil)
	if err != nil {
		if strings.Contains(err.Error(), "ResourceNotFound") {
			klog.V(2).Infof("Cluster %s not found!", amlFilesystemName)
			return ClusterStateNotFound, nil
		}

		klog.Warningf("error when retrieving the aml filesystem: %v", err)
		return "", convertHTTPResponseErrorToGrpcCodeError(err)
	}

	if resp.Properties != nil && resp.Properties.ProvisioningState != nil {
		if *resp.Properties.ProvisioningState == armstoragecache.AmlFilesystemProvisioningStateTypeDeleting {
			return ClusterStateDeleting, nil
		} else if *resp.Properties.ProvisioningState == armstoragecache.AmlFilesystemProvisioningStateTypeFailed {
			return ClusterStateFailed, nil
		}
	}

	return ClusterStateExists, nil
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

func (d *DynamicProvisioner) CreateAmlFilesystem(ctx context.Context, amlFilesystemProperties *AmlFilesystemProperties) (string, error) {
	if d.amlFilesystemsClient == nil {
		return "", status.Error(codes.Internal, "aml filesystem client is nil")
	}
	if amlFilesystemProperties.SubnetInfo.SubnetID == "" || amlFilesystemProperties.SubnetInfo.SubnetName == "" || amlFilesystemProperties.SubnetInfo.VnetName == "" || amlFilesystemProperties.SubnetInfo.VnetResourceGroup == "" {
		return "", status.Error(codes.InvalidArgument, "invalid subnet info, must have valid subnet ID, subnet name, vnet name, and vnet resource group")
	}

	tags := make(map[string]*string, len(amlFilesystemProperties.Tags))
	for key, value := range amlFilesystemProperties.Tags {
		tags[key] = to.Ptr(value)
	}
	zones := make([]*string, len(amlFilesystemProperties.Zones))
	for i, zone := range amlFilesystemProperties.Zones {
		zones[i] = to.Ptr(zone)
	}
	properties := &armstoragecache.AmlFilesystemProperties{
		FilesystemSubnet: to.Ptr(amlFilesystemProperties.SubnetInfo.SubnetID),
		MaintenanceWindow: &armstoragecache.AmlFilesystemPropertiesMaintenanceWindow{
			DayOfWeek:    to.Ptr(amlFilesystemProperties.MaintenanceDayOfWeek),
			TimeOfDayUTC: to.Ptr(amlFilesystemProperties.TimeOfDayUTC),
		},
		StorageCapacityTiB: to.Ptr(amlFilesystemProperties.StorageCapacityTiB),
	}
	amlFilesystem := armstoragecache.AmlFilesystem{
		Location:   to.Ptr(amlFilesystemProperties.Location),
		Tags:       tags,
		Properties: properties,
		Zones:      zones,
		SKU:        &armstoragecache.SKUName{Name: to.Ptr(amlFilesystemProperties.SKUName)},
	}
	if amlFilesystemProperties.Identities != nil {
		userAssignedIdentities := make(map[string]*armstoragecache.UserAssignedIdentitiesValue, len(amlFilesystemProperties.Identities))
		for _, identity := range amlFilesystemProperties.Identities {
			userAssignedIdentities[identity] = &armstoragecache.UserAssignedIdentitiesValue{}
		}
		amlFilesystem.Identity = &armstoragecache.AmlFilesystemIdentity{
			Type:                   to.Ptr(armstoragecache.AmlFilesystemIdentityTypeUserAssigned),
			UserAssignedIdentities: userAssignedIdentities,
		}
	}

	currentClusterState, err := d.currentClusterState(ctx, amlFilesystemProperties.ResourceGroupName, amlFilesystemProperties.AmlFilesystemName)
	if err != nil {
		return "", convertHTTPResponseErrorToGrpcCodeError(err)
	}

	switch currentClusterState {
	case ClusterStateNotFound:
		hasSufficientCapacity, err := d.CheckSubnetCapacity(ctx, amlFilesystemProperties.SubnetInfo, amlFilesystemProperties.SKUName, amlFilesystemProperties.StorageCapacityTiB)
		if err != nil {
			return "", convertHTTPResponseErrorToGrpcCodeError(err)
		}
		if !hasSufficientCapacity {
			return "", status.Errorf(codes.ResourceExhausted, "cannot create AMLFS cluster %s in subnet %s, not enough IP addresses available",
				amlFilesystemProperties.AmlFilesystemName,
				amlFilesystemProperties.SubnetInfo.SubnetID,
			)
		}
	case ClusterStateDeleting:
		return "", status.Errorf(codes.Aborted, "AMLFS cluster %s creation did not complete correctly, waiting for deletion to complete before retrying cluster creation",
			amlFilesystemProperties.AmlFilesystemName)
	case ClusterStateFailed:
		klog.V(2).Infof("AMLFS cluster %s is in a failed state, will attempt to correct on new creation", amlFilesystemProperties.AmlFilesystemName)
	case ClusterStateExists:
		// TODO: if we allow reusing AMLFS clusters, we should check  the existing cluster's properties
		klog.V(2).Infof("AMLFS cluster %s already exists, will attempt update request", amlFilesystemProperties.AmlFilesystemName)
	}

	klog.V(2).Infof("creating AMLFS cluster: %#v", amlFilesystemProperties)
	poller, err := d.amlFilesystemsClient.BeginCreateOrUpdate(
		ctx,
		amlFilesystemProperties.ResourceGroupName,
		amlFilesystemProperties.AmlFilesystemName,
		amlFilesystem,
		nil)
	if err != nil {
		retry, retryErr := d.checkErrorForRetry(ctx, err, amlFilesystemProperties)
		if retryErr != nil {
			return "", convertHTTPResponseErrorToGrpcCodeError(retryErr)
		}
		if retry {
			return "", d.tryDeleteBeforeRetry(ctx, amlFilesystemProperties)
		}
		return "", convertHTTPResponseErrorToGrpcCodeError(err)
	}

	pollerOptions := &runtime.PollUntilDoneOptions{
		Frequency: d.pollFrequency,
	}
	res, err := poller.PollUntilDone(ctx, pollerOptions)
	if err != nil {
		retry, retryErr := d.checkErrorForRetry(ctx, err, amlFilesystemProperties)
		if retryErr != nil {
			return "", convertHTTPResponseErrorToGrpcCodeError(retryErr)
		}
		if retry {
			return "", d.tryDeleteBeforeRetry(ctx, amlFilesystemProperties)
		}
		klog.Errorf("failed to poll the result: %v", err)
		return "", convertHTTPResponseErrorToGrpcCodeError(err)
	}

	klog.V(2).Infof("Successfully created AML filesystem: %s", amlFilesystemProperties.AmlFilesystemName)
	mgsAddress := *res.Properties.ClientInfo.MgsAddress
	return mgsAddress, nil
}

func (d *DynamicProvisioner) tryDeleteBeforeRetry(ctx context.Context, amlFilesystemProperties *AmlFilesystemProperties) error {
	resourceGroupName := amlFilesystemProperties.ResourceGroupName
	amlFilesystemName := amlFilesystemProperties.AmlFilesystemName
	err := d.DeleteAmlFilesystem(ctx, resourceGroupName, amlFilesystemName)
	if err != nil {
		klog.Errorf("error attempting to delete AMLFS cluster %s for creation retry: %v", amlFilesystemProperties.AmlFilesystemName, err)
		return convertHTTPResponseErrorToGrpcCodeError(err)
	}
	return status.Errorf(codes.Aborted, "AMLFS cluster %s creation timed out. Deleted failed cluster, retrying cluster creation", amlFilesystemProperties.AmlFilesystemName)
}

func (d *DynamicProvisioner) checkErrorForRetry(ctx context.Context, err error, amlFilesystemProperties *AmlFilesystemProperties) (bool, error) {
	err = convertHTTPResponseErrorToGrpcCodeError(err)
	errCode := status.Code(err)

	if errCode == codes.DeadlineExceeded && strings.Contains(err.Error(), "CreateTimeout") {
		klog.Warningf("AMLFS creation failed due to a creation timeout error, deleting and recreating AMLFS cluster: %v", err)
		return true, nil
	}

	if errCode == codes.DeadlineExceeded && strings.Contains(err.Error(), "InternalExecutionError") {
		currentClusterState, err := d.currentClusterState(ctx, amlFilesystemProperties.ResourceGroupName, amlFilesystemProperties.AmlFilesystemName)
		if err != nil {
			klog.Errorf("error getting current cluster state for cluster %s: %v", amlFilesystemProperties.AmlFilesystemName, err)
			return false, err
		}
		if currentClusterState == ClusterStateFailed {
			klog.Warningf("AMLFS creation failed due to a failed deployment, deleting and recreating AMLFS cluster: %v", err)
			return true, nil
		}
	}
	return false, nil
}

func (d *DynamicProvisioner) GetSkuValuesForLocation(ctx context.Context, location string) map[string]*LustreSkuValue {
	if d.skusClient == nil {
		klog.Warning("skus client is nil, using defaults")
		return d.defaultSkuValues
	}

	skusPager := d.skusClient.NewListPager(nil)
	skuValues := make(map[string]*LustreSkuValue)

	var amlfsSkus []*armstoragecache.ResourceSKU
	var skusForLocation []*armstoragecache.ResourceSKU

	for skusPager.More() {
		page, err := skusPager.NextPage(ctx)
		if err != nil {
			klog.Errorf("error getting SKUs for location %s, using defaults: %v", location, err)
			return d.defaultSkuValues
		}

		for _, sku := range page.Value {
			if *sku.ResourceType == AmlfsSkuResourceType {
				amlfsSkus = append(amlfsSkus, sku)
			}
		}
	}

	if len(amlfsSkus) == 0 {
		klog.Warning("no AMLFS SKUs found, using defaults")
		return d.defaultSkuValues
	}

	for _, sku := range amlfsSkus {
		for _, skuLocation := range sku.Locations {
			if strings.EqualFold(*skuLocation, location) {
				skusForLocation = append(skusForLocation, sku)
			}
		}
	}

	if len(skusForLocation) == 0 {
		klog.Warningf("found no AMLFS SKUs for location %s, using defaults", location)
		return d.defaultSkuValues
	}

	for _, sku := range skusForLocation {
		var incrementInTib int64
		var maximumInTib int64
		for _, capability := range sku.Capabilities {
			if *capability.Name == AmlfsSkuCapacityIncrementName {
				parsedValue, err := strconv.ParseInt(*capability.Value, 10, 64)
				if err != nil {
					klog.Errorf("failed to parse capability value: %v", err)
					continue
				}
				incrementInTib = parsedValue
			} else if *capability.Name == AmlfsSkuCapacityMaximumName {
				parsedValue, err := strconv.ParseInt(*capability.Value, 10, 64)
				if err != nil {
					klog.Errorf("failed to parse capability value: %v", err)
					continue
				}
				maximumInTib = parsedValue
			}
		}
		if incrementInTib != 0 && maximumInTib != 0 {
			skuValues[*sku.Name] = &LustreSkuValue{
				IncrementInTib: incrementInTib,
				MaximumInTib:   maximumInTib,
			}
		}
	}

	if len(skuValues) == 0 {
		klog.Warningf("found no AMLFS SKUs for location %s, using defaults", location)
		return d.defaultSkuValues
	}

	return skuValues
}

func (d *DynamicProvisioner) getAmlfsSubnetSize(ctx context.Context, sku string, clusterSize float32) (int, error) {
	if d.mgmtClient == nil {
		return 0, status.Error(codes.Internal, "storage management client is nil")
	}

	reqSize, err := d.mgmtClient.GetRequiredAmlFSSubnetsSize(ctx, &armstoragecache.ManagementClientGetRequiredAmlFSSubnetsSizeOptions{
		RequiredAMLFilesystemSubnetsSizeInfo: &armstoragecache.RequiredAmlFilesystemSubnetsSizeInfo{
			SKU: &armstoragecache.SKUName{
				Name: to.Ptr(sku),
			},
			StorageCapacityTiB: to.Ptr(clusterSize),
		},
	})
	if err != nil {
		klog.Errorf("failed to get required AMLFS subnet size for SKU: %s, cluster size: %f, error: %v", sku, clusterSize, err)
		return 0, convertHTTPResponseErrorToGrpcCodeError(err)
	}

	return int(*reqSize.RequiredAmlFilesystemSubnetsSize.FilesystemSubnetSize), nil
}

func (d *DynamicProvisioner) checkSubnetAddresses(ctx context.Context, vnetResourceGroup, vnetName, subnetID string) (int, error) {
	if d.vnetClient == nil {
		return 0, status.Error(codes.Internal, "vnet client is nil")
	}
	usagesPager := d.vnetClient.NewListUsagePager(vnetResourceGroup, vnetName, nil)

	for usagesPager.More() {
		page, err := usagesPager.NextPage(ctx)
		if err != nil {
			klog.Errorf("error getting next page: %v", err)
			return 0, convertHTTPResponseErrorToGrpcCodeError(err)
		}

		for _, usageValue := range page.Value {
			if *usageValue.ID == subnetID {
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
	if err != nil {
		klog.Errorf("error getting required subnet size: %v", err)
		return false, convertHTTPResponseErrorToGrpcCodeError(err)
	}

	availableIPs, err := d.checkSubnetAddresses(ctx, subnetInfo.VnetResourceGroup, subnetInfo.VnetName, subnetInfo.SubnetID)
	if err != nil {
		klog.Errorf("error getting available IPs: %v", err)
		return false, convertHTTPResponseErrorToGrpcCodeError(err)
	}

	if requiredSubnetIPSize > availableIPs {
		klog.Warningf("There is not enough room in the %s subnetID to fit a %s SKU cluster: %v needed, %v available", subnetInfo.SubnetID, sku, requiredSubnetIPSize, availableIPs)
		return false, nil
	}
	klog.V(2).Infof("There is enough room in the %s subnetID to fit a %s SKU cluster: %v needed, %v available", subnetInfo.SubnetID, sku, requiredSubnetIPSize, availableIPs)
	return true, nil
}
