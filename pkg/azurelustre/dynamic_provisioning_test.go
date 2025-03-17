package azurelustre

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	networkfake "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6/fake"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mockAmlfsRecorder struct {
	recordedAmlfsConfigurations map[string]armstoragecache.AmlFilesystem
	failureBehaviors            []string
	fakeCallCount               []string
}

const (
	expectedMgsAddress                          = "127.0.0.3"
	expectedResourceGroupName                   = "fake-resource-group"
	expectedAmlFilesystemName                   = "fake-amlfs"
	expectedLocation                            = "fake-location"
	expectedAmlFilesystemSubnetSize             = 24
	expectedUsedIPCount                         = 10
	expectedFullIPCount                         = 256
	expectedTotalIPCount                        = 256
	expectedSku                                 = "fake-sku"
	expectedClusterSize                         = 48
	expectedSkuIncrement                        = "4"
	expectedSkuMaximum                          = "128"
	expectedVnetName                            = "fake-vnet"
	expectedAmlFilesystemSubnetName             = "fake-subnet-name"
	expectedAmlFilesystemSubnetID               = "fake-subnet-id"
	fullVnetName                                = "full-vnet"
	invalidSku                                  = "invalid-sku"
	missingAmlFilesystemSubnetID                = "missing-subnet-id"
	vnetListUsageErrorName                      = "vnet-list-usage-error"
	vnetNoSubnetInfoName                        = "vnet-no-subnet-info"
	immediateCreateFailureName                  = "immediate-create-failure"
	eventualCreateFailureName                   = "eventual-create-failure"
	eventualInternalExecutionCreateFailureName  = "internal-execution-with-200-create-failure"
	immediateDeleteFailureName                  = "immediate-delete-failure"
	eventualDeleteFailureName                   = "eventual-delete-failure"
	clusterGetImmediateFailureName              = "cluster-get-failure"
	clusterGetRetryCheckFailureName             = "cluster-get-retry-check-failure"
	errorLocation                               = "sku-error-location"
	noAmlfsSkus                                 = "no-amlfs-skus"
	noAmlfsSkusForLocation                      = "no-amlfs-skus-for-location"
	invalidSkuIncrement                         = "invalid-sku-increment"
	invalidSkuMaximum                           = "invalid-sku-maximum"
	immediateClusterRequestTimeoutFailureName   = "testClusterShouldImmediatelyTimeout"
	immediateInternalExecutionCreateFailureName = "testClusterCreateImmediatelyInternalError"
	clusterIsFailed                             = "testClusterGetImmediatelyInternalError"
	eventualClusterCreateTimeoutFailureName     = "testClusterShouldEventuallyTimeout"
	clusterRequestRetryDeleteFailureName        = "testClusterShouldFailRetryDelete"
	clusterIsDeleting                           = "testClusterDeleting"

	quickPollFrequency = 1 * time.Millisecond
)

func (recorder *mockAmlfsRecorder) recordFakeCall() {
	pc, _, _, ok := runtime.Caller(2)
	if !ok {
		return
	}
	fn := runtime.FuncForPC(pc)
	name := fn.Name()
	fakeFunctionRegex := regexp.MustCompile(`fake\.\(\*(?<fake>\w*)\)\.dispatch(?<function>\w*)`)
	matches := fakeFunctionRegex.FindStringSubmatch(name)
	functionName := matches[1] + "." + matches[2]
	recorder.fakeCallCount = append(recorder.fakeCallCount, functionName)
}

func buildExpectedSubnetInfo() SubnetProperties {
	return SubnetProperties{
		VnetResourceGroup: expectedResourceGroupName,
		VnetName:          expectedVnetName,
		SubnetName:        expectedAmlFilesystemSubnetName,
		SubnetID:          expectedAmlFilesystemSubnetID,
	}
}

func getNextFailureBehavior(recorder *mockAmlfsRecorder) string {
	var nextFailureBehavior string
	if len(recorder.failureBehaviors) > 0 {
		nextFailureBehavior, recorder.failureBehaviors = recorder.failureBehaviors[0], recorder.failureBehaviors[1:]
	}
	return nextFailureBehavior
}

func newMockAmlfsRecorder(failureBehaviors []string) *mockAmlfsRecorder {
	return &mockAmlfsRecorder{
		recordedAmlfsConfigurations: make(map[string]armstoragecache.AmlFilesystem),
		failureBehaviors:            failureBehaviors,
		fakeCallCount:               []string{},
	}
}

func newTestDynamicProvisioner(t *testing.T, recorder *mockAmlfsRecorder) *DynamicProvisioner {
	dynamicProvisioner := &DynamicProvisioner{
		amlFilesystemsClient: newFakeAmlFilesystemsClient(t, recorder),
		vnetClient:           newFakeVnetClient(t, recorder),
		mgmtClient:           newFakeMgmtClient(t, recorder),
		skusClient:           newFakeSkusClient(t, recorder),
		defaultSkuValues:     DefaultSkuValues,
		pollFrequency:        quickPollFrequency,
	}

	return dynamicProvisioner
}

func newFakeSkusClient(t *testing.T, recorder *mockAmlfsRecorder) *armstoragecache.SKUsClient {
	skusClientFactory, err := armstoragecache.NewClientFactory("fake-subscription-id", &azfake.TokenCredential{},
		&arm.ClientOptions{
			ClientOptions: azcore.ClientOptions{
				Transport: fake.NewSKUsServerTransport(newFakeSkusServer(t, recorder)),
			},
		},
	)
	require.NoError(t, err)
	require.NotNil(t, skusClientFactory)

	fakeSkusClient := skusClientFactory.NewSKUsClient()
	require.NotNil(t, fakeSkusClient)

	return fakeSkusClient
}

func newResourceSku(resourceType, skuName, location, increment, maximum string) *armstoragecache.ResourceSKU {
	resourceSku := &armstoragecache.ResourceSKU{
		ResourceType: to.Ptr(resourceType),
	}
	if resourceType == AmlfsSkuResourceType {
		resourceSku.Name = to.Ptr(skuName)
		resourceSku.Locations = []*string{to.Ptr(location)}
		resourceSku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{
			{
				Name:  to.Ptr("OSS capacity increment (TiB)"),
				Value: to.Ptr(increment),
			},
			{
				Name:  to.Ptr("bandwidth increment (MB/s/TiB)"),
				Value: to.Ptr("500"),
			},
			{
				Name:  to.Ptr("durable"),
				Value: to.Ptr("True"),
			},
			{
				Name:  to.Ptr("MDS capacity increment (TiB)"),
				Value: to.Ptr("1024"),
			},
			{
				Name:  to.Ptr("default maximum capacity (TiB)"),
				Value: to.Ptr(maximum),
			},
			{
				Name:  to.Ptr("large cluster maximum capacity (TiB)"),
				Value: to.Ptr("1024"),
			},
			{
				Name:  to.Ptr("large cluster XL maximum capacity (TiB)"),
				Value: to.Ptr("1024"),
			},
		}
	}
	return resourceSku
}

func newFakeSkusServer(_ *testing.T, recorder *mockAmlfsRecorder) *fake.SKUsServer {
	fakeSkusServer := fake.SKUsServer{}

	fakeSkusServer.NewListPager = func(_ *armstoragecache.SKUsClientListOptions) azfake.PagerResponder[armstoragecache.SKUsClientListResponse] {
		recorder.recordFakeCall()
		resp := azfake.PagerResponder[armstoragecache.SKUsClientListResponse]{}
		nextFailureBehavior := getNextFailureBehavior(recorder)
		if nextFailureBehavior != "" {
			switch nextFailureBehavior {
			case errorLocation:
				resp.AddError(errors.New("fake location error"))
				return resp
			case noAmlfsSkus:
				resp.AddPage(http.StatusOK, armstoragecache.SKUsClientListResponse{
					ResourceSKUsResult: armstoragecache.ResourceSKUsResult{
						Value: []*armstoragecache.ResourceSKU{
							newResourceSku("caches", "", expectedLocation, "", ""),
						},
					},
				}, nil)
				return resp
			case noAmlfsSkusForLocation:
				otherSkuLocation := "other-sku-location"
				resp.AddPage(http.StatusOK, armstoragecache.SKUsClientListResponse{
					ResourceSKUsResult: armstoragecache.ResourceSKUsResult{
						Value: []*armstoragecache.ResourceSKU{
							newResourceSku(AmlfsSkuResourceType,
								expectedSku,
								otherSkuLocation,
								expectedSkuIncrement,
								expectedSkuMaximum),
						},
					},
				}, nil)
				return resp
			case invalidSkuIncrement:
				invalidSkuIncrementValue := "a"
				resp.AddPage(http.StatusOK, armstoragecache.SKUsClientListResponse{
					ResourceSKUsResult: armstoragecache.ResourceSKUsResult{
						Value: []*armstoragecache.ResourceSKU{
							newResourceSku(AmlfsSkuResourceType,
								expectedSku,
								expectedLocation,
								invalidSkuIncrementValue,
								expectedSkuMaximum),
						},
					},
				}, nil)
				return resp
			case invalidSkuMaximum:
				invalidSkuMaximumValue := "a"
				resp.AddPage(http.StatusOK, armstoragecache.SKUsClientListResponse{
					ResourceSKUsResult: armstoragecache.ResourceSKUsResult{
						Value: []*armstoragecache.ResourceSKU{
							newResourceSku(AmlfsSkuResourceType,
								expectedSku,
								expectedLocation,
								expectedSkuIncrement,
								invalidSkuMaximumValue),
						},
					},
				}, nil)
				return resp
			}
		}
		resp.AddPage(http.StatusOK, armstoragecache.SKUsClientListResponse{
			ResourceSKUsResult: armstoragecache.ResourceSKUsResult{
				Value: []*armstoragecache.ResourceSKU{
					newResourceSku("caches", "", expectedLocation, "", ""),
					newResourceSku(AmlfsSkuResourceType,
						expectedSku,
						expectedLocation,
						expectedSkuIncrement,
						expectedSkuMaximum),
				},
			},
		}, nil)
		return resp
	}
	return &fakeSkusServer
}

func newFakeVnetClient(t *testing.T, recorder *mockAmlfsRecorder) *armnetwork.VirtualNetworksClient {
	vnetClientFactory, err := armnetwork.NewClientFactory("fake-subscription-id", &azfake.TokenCredential{},
		&arm.ClientOptions{
			ClientOptions: azcore.ClientOptions{
				Transport: networkfake.NewVirtualNetworksServerTransport(newFakeVnetServer(t, recorder)),
			},
		},
	)
	require.NoError(t, err)
	require.NotNil(t, vnetClientFactory)

	fakeVnetClient := vnetClientFactory.NewVirtualNetworksClient()
	require.NotNil(t, fakeVnetClient)

	return fakeVnetClient
}

func newFakeVnetServer(_ *testing.T, recorder *mockAmlfsRecorder) *networkfake.VirtualNetworksServer {
	fakeVnetServer := networkfake.VirtualNetworksServer{}

	fakeVnetServer.NewListUsagePager = func(_, vnetName string, _ *armnetwork.VirtualNetworksClientListUsageOptions) azfake.PagerResponder[armnetwork.VirtualNetworksClientListUsageResponse] {
		recorder.recordFakeCall()
		resp := azfake.PagerResponder[armnetwork.VirtualNetworksClientListUsageResponse]{}

		if vnetName == vnetListUsageErrorName {
			resp.AddError(errors.New("fake vnet list usage error"))
			return resp
		}

		if vnetName == vnetNoSubnetInfoName {
			resp.AddPage(http.StatusOK, armnetwork.VirtualNetworksClientListUsageResponse{
				VirtualNetworkListUsageResult: armnetwork.VirtualNetworkListUsageResult{
					Value: []*armnetwork.VirtualNetworkUsage{},
				},
			}, nil)
			return resp
		}

		usedIPCount := expectedUsedIPCount
		if vnetName == fullVnetName {
			usedIPCount = expectedFullIPCount
		}
		resp.AddPage(http.StatusOK, armnetwork.VirtualNetworksClientListUsageResponse{
			VirtualNetworkListUsageResult: armnetwork.VirtualNetworkListUsageResult{
				Value: []*armnetwork.VirtualNetworkUsage{
					{
						ID:           to.Ptr(string("other-" + expectedAmlFilesystemName)),
						CurrentValue: to.Ptr(float64(usedIPCount)),
						Limit:        to.Ptr(float64(expectedTotalIPCount)),
					},
				},
			},
		}, nil)
		resp.AddPage(http.StatusOK, armnetwork.VirtualNetworksClientListUsageResponse{
			VirtualNetworkListUsageResult: armnetwork.VirtualNetworkListUsageResult{
				Value: []*armnetwork.VirtualNetworkUsage{
					{
						ID:           to.Ptr(string("another" + expectedAmlFilesystemSubnetID)),
						CurrentValue: to.Ptr(float64(usedIPCount)),
						Limit:        to.Ptr(float64(expectedTotalIPCount)),
					},
					{
						ID:           to.Ptr(string(expectedAmlFilesystemSubnetID)),
						CurrentValue: to.Ptr(float64(usedIPCount)),
						Limit:        to.Ptr(float64(expectedTotalIPCount)),
					},
				},
			},
		}, nil)
		return resp
	}
	return &fakeVnetServer
}

func newFakeMgmtClient(t *testing.T, recorder *mockAmlfsRecorder) *armstoragecache.ManagementClient {
	mgmtClientFactory, err := armstoragecache.NewClientFactory("fake-subscription-id", &azfake.TokenCredential{},
		&arm.ClientOptions{
			ClientOptions: azcore.ClientOptions{
				Transport: fake.NewManagementServerTransport(newFakeManagementServer(t, recorder)),
			},
		},
	)
	require.NoError(t, err)
	require.NotNil(t, mgmtClientFactory)

	fakeMgmtClient := mgmtClientFactory.NewManagementClient()
	require.NotNil(t, fakeMgmtClient)

	return fakeMgmtClient
}

func newFakeManagementServer(_ *testing.T, recorder *mockAmlfsRecorder) *fake.ManagementServer {
	fakeMgmtServer := fake.ManagementServer{}

	fakeMgmtServer.GetRequiredAmlFSSubnetsSize = func(_ context.Context, options *armstoragecache.ManagementClientGetRequiredAmlFSSubnetsSizeOptions) (azfake.Responder[armstoragecache.ManagementClientGetRequiredAmlFSSubnetsSizeResponse], azfake.ErrorResponder) {
		recorder.recordFakeCall()
		errResp := azfake.ErrorResponder{}
		resp := azfake.Responder[armstoragecache.ManagementClientGetRequiredAmlFSSubnetsSizeResponse]{}
		if *options.RequiredAMLFilesystemSubnetsSizeInfo.SKU.Name == invalidSku {
			errResp.SetError(errors.New("fake invalid sku error"))
			return resp, errResp
		}
		resp.SetResponse(http.StatusOK, armstoragecache.ManagementClientGetRequiredAmlFSSubnetsSizeResponse{
			RequiredAmlFilesystemSubnetsSize: armstoragecache.RequiredAmlFilesystemSubnetsSize{
				FilesystemSubnetSize: to.Ptr(int32(expectedAmlFilesystemSubnetSize)),
			},
		}, nil)
		return resp, errResp
	}
	return &fakeMgmtServer
}

func createTimeoutErrorResponse() *azcore.ResponseError {
	e := &azcore.ResponseError{}
	err := e.UnmarshalJSON([]byte(
		`{
			"errorCode": "CreateTimeout",
			"statusCode": 200,
			"errorMessage": "Amlfilesystem creation did not complete because an operation timed out.  Delete this amlfilesystem and try again."
		}`,
	))
	if err != nil {
		return &azcore.ResponseError{StatusCode: http.StatusInternalServerError, ErrorCode: "UNEXPECTED_TEST_FAILURE"}
	}
	return e
}

func createInternalExecutionErrorResponse() *azcore.ResponseError {
	e := &azcore.ResponseError{}
	err := e.UnmarshalJSON([]byte(
		`{
			"errorCode": "InternalExecutionError",
			"statusCode": 200,
			"errorMessage": "An internal execution error occurred. Please retry later."
		}`,
	))
	if err != nil {
		return &azcore.ResponseError{StatusCode: http.StatusInternalServerError, ErrorCode: "UNEXPECTED_TEST_FAILURE"}
	}
	return e
}

func newFakeAmlFilesystemsClient(t *testing.T, recorder *mockAmlfsRecorder) *armstoragecache.AmlFilesystemsClient {
	amlFilesystemsClientFactory, err := armstoragecache.NewClientFactory("fake-subscription-id", &azfake.TokenCredential{},
		&arm.ClientOptions{
			ClientOptions: azcore.ClientOptions{
				Transport: fake.NewAmlFilesystemsServerTransport(newFakeAmlFilesystemsServer(t, recorder)),
			},
		},
	)
	require.NoError(t, err)
	require.NotNil(t, amlFilesystemsClientFactory)

	fakeAmlFilesystemsClient := amlFilesystemsClientFactory.NewAmlFilesystemsClient()
	require.NotNil(t, fakeAmlFilesystemsClient)

	return fakeAmlFilesystemsClient
}

func newFakeAmlFilesystemsServer(_ *testing.T, recorder *mockAmlfsRecorder) *fake.AmlFilesystemsServer {
	fakeAmlfsServer := fake.AmlFilesystemsServer{}

	fakeAmlfsServer.BeginDelete = func(_ context.Context, _, amlFilesystemName string, _ *armstoragecache.AmlFilesystemsClientBeginDeleteOptions) (azfake.PollerResponder[armstoragecache.AmlFilesystemsClientDeleteResponse], azfake.ErrorResponder) {
		recorder.recordFakeCall()
		errResp := azfake.ErrorResponder{}
		resp := azfake.PollerResponder[armstoragecache.AmlFilesystemsClientDeleteResponse]{}
		if amlFilesystemName == immediateDeleteFailureName {
			errResp.SetError(&azcore.ResponseError{StatusCode: http.StatusConflict})
			return resp, errResp
		}

		nextFailureBehavior := getNextFailureBehavior(recorder)
		if nextFailureBehavior == clusterRequestRetryDeleteFailureName {
			errResp.SetResponseError(http.StatusInternalServerError, clusterRequestRetryDeleteFailureName)
			return resp, errResp
		}

		resp.AddNonTerminalResponse(http.StatusAccepted, nil)
		resp.AddNonTerminalResponse(http.StatusOK, nil)
		resp.AddNonTerminalResponse(http.StatusOK, nil)
		if amlFilesystemName == eventualDeleteFailureName {
			resp.SetTerminalError(http.StatusInternalServerError, eventualDeleteFailureName)
			return resp, errResp
		}

		delete(recorder.recordedAmlfsConfigurations, amlFilesystemName)

		resp.SetTerminalResponse(http.StatusOK, armstoragecache.AmlFilesystemsClientDeleteResponse{}, nil)

		return resp, errResp
	}

	fakeAmlfsServer.BeginCreateOrUpdate = func(_ context.Context, _, amlFilesystemName string, amlFilesystem armstoragecache.AmlFilesystem, _ *armstoragecache.AmlFilesystemsClientBeginCreateOrUpdateOptions) (azfake.PollerResponder[armstoragecache.AmlFilesystemsClientCreateOrUpdateResponse], azfake.ErrorResponder) {
		recorder.recordFakeCall()
		amlFilesystem.Name = to.Ptr(amlFilesystemName)
		amlFilesystem.Properties.ClientInfo = &armstoragecache.AmlFilesystemClientInfo{
			ContainerStorageInterface: (*armstoragecache.AmlFilesystemContainerStorageInterface)(nil),
			LustreVersion:             (*string)(nil),
			MgsAddress:                to.Ptr(expectedMgsAddress),
			MountCommand:              (*string)(nil),
		}

		errResp := azfake.ErrorResponder{}
		resp := azfake.PollerResponder[armstoragecache.AmlFilesystemsClientCreateOrUpdateResponse]{}
		if amlFilesystemName == immediateCreateFailureName {
			errResp.SetError(&azcore.ResponseError{StatusCode: http.StatusBadGateway})
			return resp, errResp
		}
		nextFailureBehavior := getNextFailureBehavior(recorder)
		if nextFailureBehavior == immediateClusterRequestTimeoutFailureName {
			errResp.SetError(createTimeoutErrorResponse())
			return resp, errResp
		}
		if nextFailureBehavior == immediateInternalExecutionCreateFailureName {
			errResp.SetError(createInternalExecutionErrorResponse())
			recorder.recordedAmlfsConfigurations[amlFilesystemName] = amlFilesystem
			return resp, errResp
		}

		resp.AddNonTerminalResponse(http.StatusCreated, nil)
		resp.AddNonTerminalResponse(http.StatusOK, nil)
		resp.AddNonTerminalResponse(http.StatusOK, nil)
		if amlFilesystemName == eventualCreateFailureName {
			resp.SetTerminalError(http.StatusRequestTimeout, eventualCreateFailureName)
			return resp, errResp
		}
		if nextFailureBehavior == eventualInternalExecutionCreateFailureName {
			resp.SetTerminalError(http.StatusOK, "InternalExecutionError")
			recorder.recordedAmlfsConfigurations[amlFilesystemName] = amlFilesystem
			return resp, errResp
		}
		if nextFailureBehavior == eventualClusterCreateTimeoutFailureName {
			resp.SetTerminalError(http.StatusOK, "CreateTimeout")
			recorder.recordedAmlfsConfigurations[amlFilesystemName] = amlFilesystem
			return resp, errResp
		}

		recorder.recordedAmlfsConfigurations[amlFilesystemName] = amlFilesystem
		resp.SetTerminalResponse(http.StatusOK, armstoragecache.AmlFilesystemsClientCreateOrUpdateResponse{
			AmlFilesystem: amlFilesystem,
		}, nil)
		return resp, errResp
	}

	fakeAmlfsServer.Get = func(_ context.Context, _, amlFilesystemName string, _ *armstoragecache.AmlFilesystemsClientGetOptions) (azfake.Responder[armstoragecache.AmlFilesystemsClientGetResponse], azfake.ErrorResponder) {
		recorder.recordFakeCall()
		var amlFilesystem *armstoragecache.AmlFilesystem
		errResp := azfake.ErrorResponder{}
		resp := azfake.Responder[armstoragecache.AmlFilesystemsClientGetResponse]{}
		if amlFilesystemName == clusterGetImmediateFailureName {
			errResp.SetError(errors.New(clusterGetImmediateFailureName))
			return resp, errResp
		}

		for _, amlfs := range recorder.recordedAmlfsConfigurations {
			if *amlfs.Name == amlFilesystemName {
				amlFilesystem = &amlfs
			}
		}
		if amlFilesystem == nil {
			errResp.SetError(errors.New("ResourceNotFound"))
			return resp, errResp
		}
		amlFilesystem.Properties.ProvisioningState = to.Ptr(armstoragecache.AmlFilesystemProvisioningStateTypeSucceeded)

		nextFailureBehavior := getNextFailureBehavior(recorder)
		if nextFailureBehavior == clusterIsDeleting {
			amlFilesystem.Properties.ProvisioningState = to.Ptr(armstoragecache.AmlFilesystemProvisioningStateTypeDeleting)
		} else if nextFailureBehavior == clusterIsFailed {
			amlFilesystem.Properties.ProvisioningState = to.Ptr(armstoragecache.AmlFilesystemProvisioningStateTypeFailed)
		}

		if amlFilesystemName == clusterGetRetryCheckFailureName {
			errResp.SetResponseError(http.StatusInternalServerError, clusterGetRetryCheckFailureName)
			return resp, errResp
		}

		resp.SetResponse(http.StatusOK,
			armstoragecache.AmlFilesystemsClientGetResponse{
				AmlFilesystem: *amlFilesystem,
			}, nil)
		return resp, errResp
	}
	return &fakeAmlfsServer
}

func TestDynamicProvisioner_CreateAmlFilesystem_Success(t *testing.T) {
	expectedLocation := "fake-location"
	expectedMaintenanceDayOfWeek := armstoragecache.MaintenanceDayOfWeekTypeSaturday
	expectedTimeOfDayUTC := "12:00"
	expectedStorageCapacityTiB := float32(48)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	mgsIPAddress, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName:    expectedResourceGroupName,
		AmlFilesystemName:    expectedAmlFilesystemName,
		Location:             expectedLocation,
		MaintenanceDayOfWeek: expectedMaintenanceDayOfWeek,
		TimeOfDayUTC:         expectedTimeOfDayUTC,
		SKUName:              expectedSku,
		StorageCapacityTiB:   expectedStorageCapacityTiB,
		SubnetInfo:           buildExpectedSubnetInfo(),
	})
	require.NoError(t, err)
	assert.Equal(t, expectedMgsAddress, mgsIPAddress)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	actualAmlFilesystem := recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName]
	assert.Equal(t, expectedLocation, *actualAmlFilesystem.Location)
	assert.Equal(t, expectedAmlFilesystemSubnetID, *actualAmlFilesystem.Properties.FilesystemSubnet)
	assert.Equal(t, expectedSku, *recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].SKU.Name)
	assert.Nil(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Identity)
	assert.Empty(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Zones)
	assert.Empty(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Tags)
	expectedCreateCalls := []string{
		"AmlFilesystemsServerTransport.Get",
		"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
		"VirtualNetworksServerTransport.NewListUsagePager",
		"AmlFilesystemsServerTransport.BeginCreateOrUpdate",
	}
	assert.Equal(t, expectedCreateCalls, recorder.fakeCallCount)
}

func TestDynamicProvisioner_CreateAmlFilesystem_Success_Tags(t *testing.T) {
	expectedTags := map[string]string{"tag1": "value1", "tag2": "value2"}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		Tags:              expectedTags,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	assert.Len(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Tags, len(expectedTags))
	for tagName, tagValue := range recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Tags {
		assert.Equal(t, expectedTags[tagName], *tagValue)
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_Success_Zones(t *testing.T) {
	expectedZones := []string{"zone1"}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		Zones:             expectedZones,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	assert.Len(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Zones, len(expectedZones))
	for zone := range recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Zones {
		assert.Equal(t, expectedZones[zone], *recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Zones[zone])
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_Success_Identities(t *testing.T) {
	expectedIdentities := []string{"identity1", "identity2"}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		Identities:        expectedIdentities,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	assert.Equal(t, armstoragecache.AmlFilesystemIdentityTypeUserAssigned, *recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Identity.Type)
	assert.Len(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Identity.UserAssignedIdentities, len(expectedIdentities))
	for identityKey, identityValue := range recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Identity.UserAssignedIdentities {
		assert.Equal(t, &armstoragecache.UserAssignedIdentitiesValue{}, identityValue)
		assert.Contains(t, expectedIdentities, identityKey)
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_Aborted_TriesDeleteOnImmediateClusterTimeout(t *testing.T) {
	expectedCreateCalls := []string{
		"AmlFilesystemsServerTransport.Get",
		"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
		"VirtualNetworksServerTransport.NewListUsagePager",
		"AmlFilesystemsServerTransport.BeginCreateOrUpdate",
	}

	testCases := []struct {
		desc                     string
		failureBehaviors         []string
		expectedCallsForTestCase []string
		expectedError            string
	}{
		{
			desc:             "deletes on cluster timeout",
			failureBehaviors: []string{immediateClusterRequestTimeoutFailureName},
			expectedCallsForTestCase: []string{
				"AmlFilesystemsServerTransport.BeginDelete",
			},
			expectedError: "Deleted failed cluster, retrying cluster creation",
		},
		{
			desc: "deletes on immediate cluster internal error",
			failureBehaviors: []string{
				immediateInternalExecutionCreateFailureName,
				clusterIsFailed,
			},
			expectedCallsForTestCase: []string{
				"AmlFilesystemsServerTransport.Get",
				"AmlFilesystemsServerTransport.BeginDelete",
			},
			expectedError: "Deleted failed cluster, retrying cluster creation",
		},
		{
			desc: "deletes on eventual cluster internal error with 200 create",
			failureBehaviors: []string{
				eventualInternalExecutionCreateFailureName,
				clusterIsFailed,
			},
			expectedCallsForTestCase: []string{
				"AmlFilesystemsServerTransport.Get",
				"AmlFilesystemsServerTransport.BeginDelete",
			},
			expectedError: "Deleted failed cluster, retrying cluster creation",
		},
		{
			desc: "aborts if cluster is deleting",
			failureBehaviors: []string{
				"",
				clusterIsDeleting,
			},
			expectedCallsForTestCase: []string{
				"AmlFilesystemsServerTransport.Get",
			},
			expectedError: "waiting for deletion to complete",
		},
		{
			desc: "aborts if cluster is failed",
			failureBehaviors: []string{
				"",
				clusterIsFailed,
				immediateClusterRequestTimeoutFailureName,
			},
			expectedCallsForTestCase: []string{
				"AmlFilesystemsServerTransport.Get",
				"AmlFilesystemsServerTransport.BeginCreateOrUpdate",
				"AmlFilesystemsServerTransport.BeginDelete",
			},
			expectedError: "Deleted failed cluster, retrying cluster creation",
		},
		{
			desc: "aborts on eventual cluster create timeout",
			failureBehaviors: []string{
				eventualClusterCreateTimeoutFailureName,
			},
			expectedCallsForTestCase: []string{
				"AmlFilesystemsServerTransport.BeginDelete",
			},
			expectedError: "Deleted failed cluster, retrying cluster creation",
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			recorder := newMockAmlfsRecorder(tC.failureBehaviors)
			dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

			if tC.failureBehaviors[0] == "" {
				_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
					ResourceGroupName: expectedResourceGroupName,
					AmlFilesystemName: expectedAmlFilesystemName,
					SubnetInfo:        buildExpectedSubnetInfo(),
				})
				require.NoError(t, err)
				require.Len(t, recorder.recordedAmlfsConfigurations, 1)
			}

			_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
				ResourceGroupName: expectedResourceGroupName,
				AmlFilesystemName: expectedAmlFilesystemName,
				SubnetInfo:        buildExpectedSubnetInfo(),
			})
			require.Error(t, err)
			grpcStatus, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.Aborted, grpcStatus.Code())
			require.ErrorContains(t, err, expectedAmlFilesystemName)
			require.ErrorContains(t, err, tC.expectedError)
			expectedCalls := slices.Concat(
				expectedCreateCalls,
				tC.expectedCallsForTestCase,
			)
			assert.Equal(t, expectedCalls, recorder.fakeCallCount)
			if tC.failureBehaviors[0] != "" {
				require.Empty(t, recorder.recordedAmlfsConfigurations)
			}
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_Timeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now())
	cancel()

	_, err := dynamicProvisioner.CreateAmlFilesystem(ctx, &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.DeadlineExceeded, grpcStatus.Code())
	require.ErrorContains(t, err, "context deadline exceeded")
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_FailedDeleteOnRetryForClusterTimeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	failureBehaviors := []string{
		immediateClusterRequestTimeoutFailureName,
		clusterRequestRetryDeleteFailureName,
	}
	recorder := newMockAmlfsRecorder(failureBehaviors)
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: clusterRequestRetryDeleteFailureName,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.Error(t, err)
	require.ErrorContains(t, err, clusterRequestRetryDeleteFailureName)
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
	expectedCreateCalls := []string{
		"AmlFilesystemsServerTransport.Get",
		"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
		"VirtualNetworksServerTransport.NewListUsagePager",
		"AmlFilesystemsServerTransport.BeginCreateOrUpdate",
	}
	expectedDeleteCall := []string{
		"AmlFilesystemsServerTransport.BeginDelete",
	}
	expectedCalls := slices.Concat(
		expectedCreateCalls,
		expectedDeleteCall,
	)
	assert.Equal(t, expectedCalls, recorder.fakeCallCount)
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_FailedClusterStateGetOnRetryForImmediateClusterTimeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	failureBehaviors := []string{
		immediateInternalExecutionCreateFailureName,
	}
	recorder := newMockAmlfsRecorder(failureBehaviors)
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: clusterGetRetryCheckFailureName,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, grpcStatus.Code())
	require.ErrorContains(t, err, clusterGetRetryCheckFailureName)
	expectedCreateCalls := []string{
		"AmlFilesystemsServerTransport.Get",
		"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
		"VirtualNetworksServerTransport.NewListUsagePager",
		"AmlFilesystemsServerTransport.BeginCreateOrUpdate",
	}
	expectedGetCall := []string{
		"AmlFilesystemsServerTransport.Get",
	}
	expectedCalls := slices.Concat(
		expectedCreateCalls,
		expectedGetCall,
	)
	assert.Equal(t, expectedCalls, recorder.fakeCallCount)
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_FailedClusterStateGetOnRetryForEventualClusterTimeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	failureBehaviors := []string{
		eventualInternalExecutionCreateFailureName,
	}
	recorder := newMockAmlfsRecorder(failureBehaviors)
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: clusterGetRetryCheckFailureName,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, grpcStatus.Code())
	require.ErrorContains(t, err, clusterGetRetryCheckFailureName)
	expectedCreateCalls := []string{
		"AmlFilesystemsServerTransport.Get",
		"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
		"VirtualNetworksServerTransport.NewListUsagePager",
		"AmlFilesystemsServerTransport.BeginCreateOrUpdate",
	}
	expectedGetCall := []string{
		"AmlFilesystemsServerTransport.Get",
	}
	expectedCalls := slices.Concat(
		expectedCreateCalls,
		expectedGetCall,
	)
	assert.Equal(t, expectedCalls, recorder.fakeCallCount)
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_NilClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	dynamicProvisioner.amlFilesystemsClient = nil
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        SubnetProperties{},
	})
	require.ErrorContains(t, err, "aml filesystem client is nil")
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err(t *testing.T) {
	noSubnetInfoProperties := buildExpectedSubnetInfo()
	noSubnetInfoProperties.VnetName = vnetNoSubnetInfoName

	testCases := []struct {
		desc              string
		amlFilesystemName string
		resourceGroupName string
		subnetProperties  SubnetProperties
		expectedErrors    []string
	}{
		{
			desc:              "Subnet not found",
			amlFilesystemName: eventualCreateFailureName,
			resourceGroupName: expectedResourceGroupName,
			subnetProperties:  noSubnetInfoProperties,
			expectedErrors:    []string{expectedAmlFilesystemSubnetID, "not found in vnet", vnetNoSubnetInfoName},
		},
		{
			desc:              "Immediate create failure",
			amlFilesystemName: immediateCreateFailureName,
			resourceGroupName: expectedResourceGroupName,
			subnetProperties:  buildExpectedSubnetInfo(),
			expectedErrors:    []string{immediateCreateFailureName},
		},
		{
			desc:              "Eventual create failure",
			amlFilesystemName: eventualCreateFailureName,
			resourceGroupName: expectedResourceGroupName,
			subnetProperties:  buildExpectedSubnetInfo(),
			expectedErrors:    []string{eventualCreateFailureName},
		},
		{
			desc:              "Immediate get request failure",
			amlFilesystemName: clusterGetImmediateFailureName,
			resourceGroupName: expectedResourceGroupName,
			subnetProperties:  buildExpectedSubnetInfo(),
			expectedErrors:    []string{clusterGetImmediateFailureName},
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			recorder := newMockAmlfsRecorder([]string{})
			dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
			require.Empty(t, recorder.recordedAmlfsConfigurations)

			_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
				ResourceGroupName: tC.resourceGroupName,
				AmlFilesystemName: tC.amlFilesystemName,
				SubnetInfo:        tC.subnetProperties,
			})
			for _, expectedError := range tC.expectedErrors {
				require.ErrorContains(t, err, expectedError)
			}
			assert.Empty(t, recorder.recordedAmlfsConfigurations)
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_EmptySubnetInfo(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        SubnetProperties{},
	})
	require.ErrorContains(t, err, "invalid subnet info")
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_EmptyInsufficientCapacity(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	require.Empty(t, recorder.recordedAmlfsConfigurations)

	subnetProperties := buildExpectedSubnetInfo()
	subnetProperties.VnetName = fullVnetName

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        subnetProperties,
	})
	require.ErrorContains(t, err, subnetProperties.SubnetID)
	require.ErrorContains(t, err, "not enough IP addresses available")
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_CreateAmlFilesystem_Success_NoCapacityCheckIfCurrentClusterStateBeforeCall(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	subnetProperties := buildExpectedSubnetInfo()

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        subnetProperties,
	})
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)

	currentClusterState, err := dynamicProvisioner.currentClusterState(context.Background(), expectedResourceGroupName, expectedAmlFilesystemName)
	require.NoError(t, err)
	require.Equal(t, ClusterStateExists, currentClusterState)

	subnetProperties.VnetName = fullVnetName

	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	_, err = dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        subnetProperties,
	})
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})

	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)

	err = dynamicProvisioner.DeleteAmlFilesystem(context.Background(), expectedResourceGroupName, expectedAmlFilesystemName)

	require.NoError(t, err)
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Err_NilCLient(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	dynamicProvisioner.amlFilesystemsClient = nil

	err := dynamicProvisioner.DeleteAmlFilesystem(context.Background(), expectedResourceGroupName, expectedAmlFilesystemName)
	require.ErrorContains(t, err, "aml filesystem client is nil")
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Err_Timeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now())
	cancel()
	err := dynamicProvisioner.DeleteAmlFilesystem(ctx, expectedResourceGroupName, expectedAmlFilesystemName)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.DeadlineExceeded, grpcStatus.Code())
	require.ErrorContains(t, err, "context deadline exceeded")
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Err_ImmediateFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	amlFilesystemName := immediateDeleteFailureName

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: amlFilesystemName,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)

	err = dynamicProvisioner.DeleteAmlFilesystem(context.Background(), expectedResourceGroupName, amlFilesystemName)
	require.ErrorContains(t, err, immediateDeleteFailureName)
	assert.Len(t, recorder.recordedAmlfsConfigurations, 1)
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Err_EventualFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	amlFilesystemName := eventualDeleteFailureName

	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: amlFilesystemName,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)

	err = dynamicProvisioner.DeleteAmlFilesystem(context.Background(), expectedResourceGroupName, amlFilesystemName)
	require.ErrorContains(t, err, eventualDeleteFailureName)
	assert.Len(t, recorder.recordedAmlfsConfigurations, 1)
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Success_DeletesCorrectCluster(t *testing.T) {
	otherAmlFilesystemName := expectedAmlFilesystemName + "2"

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)
	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.NoError(t, err)
	_, err = dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: otherAmlFilesystemName,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 2)

	err = dynamicProvisioner.DeleteAmlFilesystem(context.Background(), expectedResourceGroupName, expectedAmlFilesystemName)
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	assert.Equal(t, otherAmlFilesystemName, *recorder.recordedAmlfsConfigurations[otherAmlFilesystemName].Name)
}

func TestDynamicProvisioner_CurrentClusterState_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)
	_, err := dynamicProvisioner.CreateAmlFilesystem(context.Background(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        buildExpectedSubnetInfo(),
	})
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)

	currentClusterState, err := dynamicProvisioner.currentClusterState(context.Background(), expectedResourceGroupName, expectedAmlFilesystemName)
	require.NoError(t, err)
	require.Equal(t, ClusterStateExists, currentClusterState)
}

func TestDynamicProvisioner_CurrentClusterState_SuccessNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	currentClusterState, err := dynamicProvisioner.currentClusterState(context.Background(), expectedResourceGroupName, expectedAmlFilesystemName)
	require.NoError(t, err)
	require.Equal(t, ClusterStateNotFound, currentClusterState)
}

func TestDynamicProvisioner_CurrentClusterState_Err(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	amlFilesystemName := clusterGetImmediateFailureName
	_, err := dynamicProvisioner.currentClusterState(context.Background(), expectedResourceGroupName, amlFilesystemName)
	assert.ErrorContains(t, err, clusterGetImmediateFailureName)
}

func TestDynamicProvisioner_CurrentClusterState_ErrNilClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	dynamicProvisioner.amlFilesystemsClient = nil

	_, err := dynamicProvisioner.currentClusterState(context.Background(), expectedResourceGroupName, expectedAmlFilesystemName)
	assert.ErrorContains(t, err, "aml filesystem client is nil")
}

func TestDynamicProvisioner_CheckSubnetCapacity_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	hasSufficientCapacity, err := dynamicProvisioner.CheckSubnetCapacity(context.Background(), buildExpectedSubnetInfo(), expectedSku, expectedClusterSize)
	require.NoError(t, err)
	assert.True(t, hasSufficientCapacity)
}

func TestDynamicProvisioner_CheckSubnetCapacity_FullVnet(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	subnetInfo := buildExpectedSubnetInfo()
	subnetInfo.VnetName = fullVnetName
	hasSufficientCapacity, err := dynamicProvisioner.CheckSubnetCapacity(context.Background(), subnetInfo, expectedSku, expectedClusterSize)
	require.NoError(t, err)
	assert.False(t, hasSufficientCapacity)
}

func TestDynamicProvisioner_CheckSubnetCapacity_Err_NilMgmtClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	dynamicProvisioner.mgmtClient = nil

	_, err := dynamicProvisioner.CheckSubnetCapacity(context.Background(), buildExpectedSubnetInfo(), expectedSku, expectedClusterSize)
	assert.ErrorContains(t, err, "storage management client is nil")
}

func TestDynamicProvisioner_CheckSubnetCapacity_Err_NilVnetClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	dynamicProvisioner.vnetClient = nil

	_, err := dynamicProvisioner.CheckSubnetCapacity(context.Background(), buildExpectedSubnetInfo(), expectedSku, expectedClusterSize)
	assert.ErrorContains(t, err, "vnet client is nil")
}

func TestDynamicProvisioner_CheckSubnetCapacity_Err(t *testing.T) {
	vnetListUsageErrorSubnetInfo := buildExpectedSubnetInfo()
	vnetListUsageErrorSubnetInfo.VnetName = vnetListUsageErrorName

	subnetInfoNotFound := buildExpectedSubnetInfo()
	subnetInfoNotFound.SubnetID = missingAmlFilesystemSubnetID

	testCases := []struct {
		desc             string
		sku              string
		subnetProperties SubnetProperties
		expectedError    string
	}{
		{
			desc:             "Invalid SKU",
			sku:              invalidSku,
			subnetProperties: buildExpectedSubnetInfo(),
			expectedError:    "fake invalid sku error",
		},
		{
			desc:             "List usage error",
			sku:              expectedSku,
			subnetProperties: vnetListUsageErrorSubnetInfo,
			expectedError:    "fake vnet list usage error",
		},
		{
			desc:             "Subnet not found",
			sku:              expectedSku,
			subnetProperties: subnetInfoNotFound,
			expectedError:    missingAmlFilesystemSubnetID + " not found in vnet",
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			recorder := newMockAmlfsRecorder([]string{})
			dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
			require.Empty(t, recorder.recordedAmlfsConfigurations)

			_, err := dynamicProvisioner.CheckSubnetCapacity(context.Background(), tC.subnetProperties, tC.sku, expectedClusterSize)
			assert.ErrorContains(t, err, tC.expectedError)
		})
	}
}

func TestDynamicProvisioner_GetSkuValuesForLocation_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	expectedSkuValues := map[string]*LustreSkuValue{expectedSku: {IncrementInTib: 4, MaximumInTib: 128}}

	skuValues := dynamicProvisioner.GetSkuValuesForLocation(context.Background(), expectedLocation)
	t.Log(skuValues)
	require.Len(t, skuValues, 1)
	assert.Equal(t, expectedSkuValues, skuValues)
}

func TestDynamicProvisioner_GetSkuValuesForLocation_NilClientReturnsDefaults(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	dynamicProvisioner.skusClient = nil

	skuValues := dynamicProvisioner.GetSkuValuesForLocation(context.Background(), expectedLocation)
	t.Log(skuValues)
	require.Len(t, skuValues, 4)
	assert.Equal(t, DefaultSkuValues, skuValues)
	assert.NotContains(t, skuValues, expectedSku)
}

func TestDynamicProvisioner_GetSkuValuesForLocation_ErrorReturnsDefaults(t *testing.T) {
	testCases := []struct {
		desc             string
		failureBehaviors []string
	}{
		{
			desc:             "No AMLFS SKUs",
			failureBehaviors: []string{noAmlfsSkus},
		},
		{
			desc:             "No AMLFS SKUs for location",
			failureBehaviors: []string{noAmlfsSkusForLocation},
		},
		{
			desc:             "Invalid SKU increment",
			failureBehaviors: []string{invalidSkuIncrement},
		},
		{
			desc:             "Invalid SKU maximum",
			failureBehaviors: []string{invalidSkuMaximum},
		},
		{
			desc:             "Invalid location",
			failureBehaviors: []string{errorLocation},
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			recorder := newMockAmlfsRecorder(tC.failureBehaviors)
			dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

			skuValues := dynamicProvisioner.GetSkuValuesForLocation(context.Background(), expectedLocation)
			t.Log(skuValues)
			require.Len(t, skuValues, 4)
			assert.Equal(t, DefaultSkuValues, skuValues)
			assert.NotContains(t, skuValues, expectedSku)
		})
	}
}

func TestConvertStatusCodeErrorToGrpcCodeError(t *testing.T) {
	tests := []struct {
		name         string
		inputError   error
		expectedCode codes.Code
	}{
		{
			name:         "BadRequest",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusBadRequest},
			expectedCode: codes.InvalidArgument,
		},
		{
			name:         "Conflict with quota limit exceeded",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusConflict, ErrorCode: "Operation results in exceeding quota limits of resource type AmlFilesystem"},
			expectedCode: codes.ResourceExhausted,
		},
		{
			name:         "Conflict without quota limit exceeded",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusConflict},
			expectedCode: codes.InvalidArgument,
		},
		{
			name:         "NotFound",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusNotFound},
			expectedCode: codes.NotFound,
		},
		{
			name:         "Forbidden",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusForbidden},
			expectedCode: codes.PermissionDenied,
		},
		{
			name:         "Unauthorized",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusUnauthorized},
			expectedCode: codes.Unauthenticated,
		},
		{
			name:         "TooManyRequests",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusTooManyRequests},
			expectedCode: codes.Unavailable,
		},
		{
			name:         "InternalServerError",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusInternalServerError},
			expectedCode: codes.Internal,
		},
		{
			name:         "BadGateway",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusBadGateway},
			expectedCode: codes.Unavailable,
		},
		{
			name:         "ServiceUnavailable",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusServiceUnavailable},
			expectedCode: codes.Unavailable,
		},
		{
			name:         "GatewayTimeout",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusGatewayTimeout},
			expectedCode: codes.DeadlineExceeded,
		},
		{
			name:         "UnknownClientError",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusTeapot},
			expectedCode: codes.InvalidArgument,
		},
		{
			name:         "UnknownServerError",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusLoopDetected},
			expectedCode: codes.Unknown,
		},
		{
			name:         "GrpcError",
			inputError:   status.Error(codes.DeadlineExceeded, "test error"),
			expectedCode: codes.DeadlineExceeded,
		},
		{
			name:         "InternalExecutionError",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusOK, ErrorCode: "InternalExecutionError"},
			expectedCode: codes.DeadlineExceeded,
		},
		{
			name:         "CreateTimeout",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusOK, ErrorCode: "CreateTimeout"},
			expectedCode: codes.DeadlineExceeded,
		},
		{
			name:       "NilError",
			inputError: nil,
		},
		{
			name:         "NonResponseError",
			inputError:   errors.New("some other error"),
			expectedCode: codes.Unknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := convertHTTPResponseErrorToGrpcCodeError(tt.inputError)
			if tt.inputError == nil {
				require.NoError(t, err)
				return
			}
			status, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.expectedCode, status.Code())
		})
	}
}
