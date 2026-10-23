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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	networkfake "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6/fake"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4/fake"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/util"
)

type mockAmlfsRecorder struct {
	recordedAmlfsConfigurations map[string]armstoragecache.AmlFilesystem
	failureBehaviors            []string
	fakeCallCount               []string
	subnetUsageError            error
	createResponse              *armstoragecache.AmlFilesystem
	createError                 error
	createPollingError          *azcore.ResponseError
	subnetUsageResponse         *armnetwork.VirtualNetworkListUsageResult
	subnetSizeError             error
	skuResponse                 *armstoragecache.ResourceSKUsResult
	skuError                    error
	getError                    error
	deleteError                 error
	deletePollingError          *azcore.ResponseError
}

const (
	expectedMgsAddress              = "127.0.0.3"
	expectedSubscriptionID          = "fake-subscription-id"
	expectedResourceGroupName       = "fake-resource-group"
	expectedAmlFilesystemName       = "fake-amlfs"
	expectedLocation                = "fake-location"
	expectedAmlFilesystemSubnetSize = 24
	expectedUsedIPCount             = 10
	expectedFullIPCount             = 256
	expectedTotalIPCount            = 256
	expectedSku                     = "fake-sku"
	otherSkuForLocation             = "other-sku-for-location"
	expectedClusterSize             = 48
	expectedSkuIncrement            = "4"
	expectedSkuMaximum              = "128"
	expectedVnetName                = "fake-vnet"
	expectedAmlFilesystemSubnetName = "fake-subnet-name"
	expectedAmlFilesystemSubnetID   = "fake-subnet-id"
	// caseMismatchAmlFilesystemSubnetID is the same subnet ID as
	// expectedAmlFilesystemSubnetID but with different casing, used to
	// simulate the ARM VNet ListUsage API returning a differently-cased
	// resource group segment than the one the driver builds itself
	// (see GitHub issue #290).
	caseMismatchAmlFilesystemSubnetID           = "FAKE-SUBNET-ID"
	caseMismatchVnetName                        = "case-mismatch-vnet"
	fullVnetName                                = "full-vnet"
	invalidSku                                  = "invalid-sku"
	missingAmlFilesystemSubnetID                = "missing-subnet-id"
	vnetListUsageErrorName                      = "vnet-list-usage-error"
	vnetNoSubnetInfoName                        = "vnet-no-subnet-info"
	eventualInternalExecutionCreateFailureName  = "internal-execution-with-200-create-failure"
	eventualAscInternalErrorCreateFailureName   = "asc-with-200-create-failure"
	immediateDeleteFailureName                  = "immediate-delete-failure"
	eventualDeleteFailureName                   = "eventual-delete-failure"
	errorLocation                               = "sku-error-location"
	forbiddenLocation                           = "sku-forbidden-location"
	noAmlfsSkus                                 = "no-amlfs-skus"
	noAmlfsSkusForLocation                      = "no-amlfs-skus-for-location"
	invalidSkuIncrement                         = "invalid-sku-increment"
	invalidSkuMaximum                           = "invalid-sku-maximum"
	noZonesForLocation                          = "no-zones-for-location"
	noLocationInfoForSku                        = "no-location-info-for-sku"
	immediateClusterCreateTimeoutFailureName    = "testClusterShouldImmediatelyTimeout"
	immediateInternalExecutionCreateFailureName = "testClusterCreateImmediatelyInternalError"
	immediateAscInternalErrorCreateFailureName  = "testClusterCreateImmediatelyAscInternalError"
	clusterIsFailed                             = "testClusterGetImmediatelyInternalError"
	eventualClusterCreateTimeoutFailureName     = "testClusterShouldEventuallyTimeout"
	clusterIsDeleting                           = "testClusterDeleting"

	quickPollFrequency = 1 * time.Millisecond
)

var expectedZones = []string{"zone3", "zone2", "zone1"}

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

func buildExpectedAmlFilesystemProperties(name string) *AmlFilesystemProperties {
	return &AmlFilesystemProperties{
		ResourceGroupName:    expectedResourceGroupName,
		AmlFilesystemName:    name,
		Location:             expectedLocation,
		SKUName:              expectedSku,
		StorageCapacityTiB:   expectedClusterSize,
		SubnetInfo:           buildExpectedSubnetInfo(),
		MaintenanceDayOfWeek: armstoragecache.MaintenanceDayOfWeekTypeSaturday,
		TimeOfDayUTC:         "12:00",
	}
}

func readyTestAmlFilesystem(name string) armstoragecache.AmlFilesystem {
	return armstoragecache.AmlFilesystem{
		ID:       new(amlFilesystemResourceID(expectedSubscriptionID, expectedResourceGroupName, name)),
		Name:     new(name),
		Location: new(expectedLocation),
		SKU:      &armstoragecache.SKUName{Name: new(expectedSku)},
		Properties: &armstoragecache.AmlFilesystemProperties{
			ProvisioningState:         new(armstoragecache.AmlFilesystemProvisioningStateTypeSucceeded),
			Health:                    &armstoragecache.AmlFilesystemHealth{State: new(armstoragecache.AmlFilesystemHealthStateTypeAvailable)},
			StorageCapacityTiB:        new(float32(expectedClusterSize)),
			CurrentStorageCapacityTiB: new(float32(expectedClusterSize)),
			FilesystemSubnet:          new(expectedAmlFilesystemSubnetID),
			ClientInfo:                &armstoragecache.AmlFilesystemClientInfo{MgsAddress: new(expectedMgsAddress)},
		},
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
	t.Helper()
	dynamicProvisioner := &DynamicProvisioner{
		subscriptionID:       expectedSubscriptionID,
		amlFilesystemsClient: newFakeAmlFilesystemsClient(t, recorder),
		vnetClient:           newFakeVnetClient(t, recorder),
		mgmtClient:           newFakeMgmtClient(t, recorder),
		skusClient:           newFakeSkusClient(t, recorder),
		pollFrequency:        quickPollFrequency,
	}

	return dynamicProvisioner
}

func newFakeSkusClient(t *testing.T, recorder *mockAmlfsRecorder) *armstoragecache.SKUsClient {
	t.Helper()
	skusClientFactory, err := armstoragecache.NewClientFactory(
		"fake-subscription-id", &azfake.TokenCredential{},
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

func newResourceSku(resourceType, skuName, location, increment, maximum string, zones []string) *armstoragecache.ResourceSKU {
	resourceSku := &armstoragecache.ResourceSKU{
		ResourceType: new(resourceType),
	}
	if resourceType == AmlfsSkuResourceType {
		resourceSku.Name = new(skuName)
		resourceSku.Locations = []*string{new(location)}
		resourceSku.LocationInfo = []*armstoragecache.ResourceSKULocationInfo{
			{
				Location: new(location),
			},
		}
		if zones != nil {
			zonePointers := make([]*string, 0, len(zones))
			for _, zone := range zones {
				zonePointers = append(zonePointers, new(zone))
			}
			resourceSku.LocationInfo[0].Zones = zonePointers
		}
		resourceSku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{
			{
				Name:  new("OSS capacity increment (TiB)"),
				Value: new(increment),
			},
			{
				Name:  new("bandwidth increment (MB/s/TiB)"),
				Value: new("500"),
			},
			{
				Name:  new("durable"),
				Value: new("True"),
			},
			{
				Name:  new("MDS capacity increment (TiB)"),
				Value: new("1024"),
			},
			{
				Name:  new("default maximum capacity (TiB)"),
				Value: new(maximum),
			},
			{
				Name:  new("large cluster maximum capacity (TiB)"),
				Value: new("1024"),
			},
			{
				Name:  new("large cluster XL maximum capacity (TiB)"),
				Value: new("1024"),
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
		if recorder.skuError != nil {
			resp.AddError(recorder.skuError)
			return resp
		}
		if recorder.skuResponse != nil {
			resp.AddPage(http.StatusOK, armstoragecache.SKUsClientListResponse{
				ResourceSKUsResult: *recorder.skuResponse,
			}, nil)
			return resp
		}
		nextFailureBehavior := getNextFailureBehavior(recorder)
		if nextFailureBehavior != "" {
			switch nextFailureBehavior {
			case errorLocation:
				resp.AddError(errors.New("fake location error"))
				return resp
			case forbiddenLocation:
				resp.AddError(&azcore.ResponseError{StatusCode: http.StatusForbidden})
				return resp
			case noAmlfsSkus:
				resp.AddPage(http.StatusOK, armstoragecache.SKUsClientListResponse{
					ResourceSKUsResult: armstoragecache.ResourceSKUsResult{
						Value: []*armstoragecache.ResourceSKU{
							newResourceSku("caches", "", expectedLocation, "", "", []string{}),
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
								expectedSkuMaximum,
								expectedZones),
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
								expectedSkuMaximum,
								expectedZones),
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
								invalidSkuMaximumValue,
								expectedZones),
						},
					},
				}, nil)
				return resp
			case noZonesForLocation:
				resp.AddPage(http.StatusOK, armstoragecache.SKUsClientListResponse{
					ResourceSKUsResult: armstoragecache.ResourceSKUsResult{
						Value: []*armstoragecache.ResourceSKU{
							newResourceSku(AmlfsSkuResourceType,
								expectedSku,
								expectedLocation,
								expectedSkuIncrement,
								expectedSkuMaximum,
								[]string{}),
						},
					},
				}, nil)
				return resp
			case noLocationInfoForSku:
				resp.AddPage(http.StatusOK, armstoragecache.SKUsClientListResponse{
					ResourceSKUsResult: armstoragecache.ResourceSKUsResult{
						Value: []*armstoragecache.ResourceSKU{
							newResourceSku(
								AmlfsSkuResourceType,
								expectedSku,
								expectedLocation,
								expectedSkuIncrement,
								expectedSkuMaximum,
								nil,
							),
						},
					},
				}, nil)
				return resp
			}
		}
		resp.AddPage(http.StatusOK, armstoragecache.SKUsClientListResponse{
			ResourceSKUsResult: armstoragecache.ResourceSKUsResult{
				Value: []*armstoragecache.ResourceSKU{
					newResourceSku("caches", "", expectedLocation, "", "", []string{}),
					newResourceSku(AmlfsSkuResourceType,
						otherSkuForLocation,
						expectedLocation,
						expectedSkuIncrement,
						expectedSkuMaximum,
						expectedZones),
					newResourceSku(AmlfsSkuResourceType,
						expectedSku,
						expectedLocation,
						expectedSkuIncrement,
						expectedSkuMaximum,
						expectedZones),
				},
			},
		}, nil)
		return resp
	}
	return &fakeSkusServer
}

func newFakeVnetClient(t *testing.T, recorder *mockAmlfsRecorder) *armnetwork.VirtualNetworksClient {
	t.Helper()
	transport := networkfake.NewVirtualNetworksServerTransport(newFakeVnetServer(t, recorder))
	vnetClientFactory, err := armnetwork.NewClientFactory(
		"fake-subscription-id", &azfake.TokenCredential{},
		&arm.ClientOptions{
			ClientOptions: azcore.ClientOptions{
				Transport: subnetCapacityTransportFunc(func(request *http.Request) (*http.Response, error) {
					if recorder.subnetUsageError != nil {
						// Pager errors are consumed; repeat the configured failure on each new request.
						return networkfake.NewVirtualNetworksServerTransport(newFakeVnetServer(t, recorder)).Do(request)
					}
					return transport.Do(request)
				}),
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

		if recorder.subnetUsageError != nil {
			resp.AddError(recorder.subnetUsageError)
			return resp
		}

		if recorder.subnetUsageResponse != nil {
			resp.AddPage(http.StatusOK, armnetwork.VirtualNetworksClientListUsageResponse{
				VirtualNetworkListUsageResult: *recorder.subnetUsageResponse,
			}, nil)
			return resp
		}

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

		if vnetName == caseMismatchVnetName {
			resp.AddPage(http.StatusOK, armnetwork.VirtualNetworksClientListUsageResponse{
				VirtualNetworkListUsageResult: armnetwork.VirtualNetworkListUsageResult{
					Value: []*armnetwork.VirtualNetworkUsage{
						{
							ID:           new(string(caseMismatchAmlFilesystemSubnetID)),
							CurrentValue: new(float64(expectedUsedIPCount)),
							Limit:        new(float64(expectedTotalIPCount)),
						},
					},
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
						ID:           new(string("other-" + expectedAmlFilesystemName)),
						CurrentValue: new(float64(usedIPCount)),
						Limit:        new(float64(expectedTotalIPCount)),
					},
				},
			},
		}, nil)
		resp.AddPage(http.StatusOK, armnetwork.VirtualNetworksClientListUsageResponse{
			VirtualNetworkListUsageResult: armnetwork.VirtualNetworkListUsageResult{
				Value: []*armnetwork.VirtualNetworkUsage{
					{
						ID:           new(string("another" + expectedAmlFilesystemSubnetID)),
						CurrentValue: new(float64(usedIPCount)),
						Limit:        new(float64(expectedTotalIPCount)),
					},
					{
						ID:           new(string(expectedAmlFilesystemSubnetID)),
						CurrentValue: new(float64(usedIPCount)),
						Limit:        new(float64(expectedTotalIPCount)),
					},
				},
			},
		}, nil)
		return resp
	}
	return &fakeVnetServer
}

func newFakeMgmtClient(t *testing.T, recorder *mockAmlfsRecorder) *armstoragecache.ManagementClient {
	t.Helper()
	mgmtClientFactory, err := armstoragecache.NewClientFactory(
		"fake-subscription-id", &azfake.TokenCredential{},
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
		if recorder.subnetSizeError != nil {
			errResp.SetError(recorder.subnetSizeError)
			return resp, errResp
		}
		if *options.RequiredAMLFilesystemSubnetsSizeInfo.SKU.Name == invalidSku {
			errResp.SetError(errors.New("fake invalid sku error"))
			return resp, errResp
		}
		resp.SetResponse(http.StatusOK, armstoragecache.ManagementClientGetRequiredAmlFSSubnetsSizeResponse{
			RequiredAmlFilesystemSubnetsSize: armstoragecache.RequiredAmlFilesystemSubnetsSize{
				FilesystemSubnetSize: new(int32(expectedAmlFilesystemSubnetSize)),
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

func createAscInternalErrorResponse() *azcore.ResponseError {
	e := &azcore.ResponseError{}
	err := e.UnmarshalJSON([]byte(
		`{
			"errorCode": "AscInternalError",
			"statusCode": 200,
			"errorMessage": "Provisioning timed out deploying Cluster VMs"
		}`,
	))
	if err != nil {
		return &azcore.ResponseError{StatusCode: http.StatusInternalServerError, ErrorCode: "UNEXPECTED_TEST_FAILURE"}
	}
	return e
}

func newFakeAmlFilesystemsClient(t *testing.T, recorder *mockAmlfsRecorder) *armstoragecache.AmlFilesystemsClient {
	t.Helper()
	amlFilesystemsClientFactory, err := armstoragecache.NewClientFactory(
		"fake-subscription-id", &azfake.TokenCredential{},
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
		if recorder.deleteError != nil {
			errResp.SetError(recorder.deleteError)
			return resp, errResp
		}
		if amlFilesystemName == immediateDeleteFailureName {
			errResp.SetError(&azcore.ResponseError{StatusCode: http.StatusConflict})
			return resp, errResp
		}

		if _, exists := recorder.recordedAmlfsConfigurations[amlFilesystemName]; !exists {
			resp.SetTerminalResponse(http.StatusNoContent, armstoragecache.AmlFilesystemsClientDeleteResponse{}, nil)
			return resp, errResp
		}

		resp.AddNonTerminalResponse(http.StatusAccepted, nil)
		resp.AddNonTerminalResponse(http.StatusOK, nil)
		resp.AddNonTerminalResponse(http.StatusOK, nil)
		if recorder.deletePollingError != nil {
			resp.SetTerminalError(recorder.deletePollingError.StatusCode, recorder.deletePollingError.ErrorCode)
			return resp, errResp
		}
		if amlFilesystemName == eventualDeleteFailureName {
			resp.SetTerminalError(http.StatusInternalServerError, eventualDeleteFailureName)
			return resp, errResp
		}

		delete(recorder.recordedAmlfsConfigurations, amlFilesystemName)

		resp.SetTerminalResponse(http.StatusOK, armstoragecache.AmlFilesystemsClientDeleteResponse{}, nil)

		return resp, errResp
	}

	fakeAmlfsServer.BeginCreateOrUpdate = func(_ context.Context, resourceGroupName, amlFilesystemName string, amlFilesystem armstoragecache.AmlFilesystem, _ *armstoragecache.AmlFilesystemsClientBeginCreateOrUpdateOptions) (azfake.PollerResponder[armstoragecache.AmlFilesystemsClientCreateOrUpdateResponse], azfake.ErrorResponder) {
		recorder.recordFakeCall()
		amlFilesystem.Name = new(amlFilesystemName)
		amlFilesystem.ID = new(amlFilesystemResourceID(expectedSubscriptionID, resourceGroupName, amlFilesystemName))
		amlFilesystem.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeSucceeded)
		amlFilesystem.Properties.Health = &armstoragecache.AmlFilesystemHealth{State: new(armstoragecache.AmlFilesystemHealthStateTypeAvailable)}
		amlFilesystem.Properties.CurrentStorageCapacityTiB = amlFilesystem.Properties.StorageCapacityTiB
		amlFilesystem.Properties.ClientInfo = &armstoragecache.AmlFilesystemClientInfo{
			ContainerStorageInterface: (*armstoragecache.AmlFilesystemContainerStorageInterface)(nil),
			LustreVersion:             (*string)(nil),
			MgsAddress:                new(expectedMgsAddress),
			MountCommand:              (*string)(nil),
		}

		errResp := azfake.ErrorResponder{}
		resp := azfake.PollerResponder[armstoragecache.AmlFilesystemsClientCreateOrUpdateResponse]{}
		if recorder.createError != nil {
			errResp.SetError(recorder.createError)
			return resp, errResp
		}
		nextFailureBehavior := getNextFailureBehavior(recorder)
		if nextFailureBehavior == immediateClusterCreateTimeoutFailureName {
			errResp.SetError(createTimeoutErrorResponse())
			return resp, errResp
		}
		if nextFailureBehavior == immediateInternalExecutionCreateFailureName {
			errResp.SetError(createInternalExecutionErrorResponse())
			recorder.recordedAmlfsConfigurations[amlFilesystemName] = amlFilesystem
			return resp, errResp
		}
		if nextFailureBehavior == immediateAscInternalErrorCreateFailureName {
			errResp.SetError(createAscInternalErrorResponse())
			recorder.recordedAmlfsConfigurations[amlFilesystemName] = amlFilesystem
			return resp, errResp
		}

		resp.AddNonTerminalResponse(http.StatusCreated, nil)
		resp.AddNonTerminalResponse(http.StatusOK, nil)
		resp.AddNonTerminalResponse(http.StatusOK, nil)
		if recorder.createPollingError != nil {
			resp.SetTerminalError(recorder.createPollingError.StatusCode, recorder.createPollingError.ErrorCode)
			return resp, errResp
		}
		if nextFailureBehavior == eventualInternalExecutionCreateFailureName {
			resp.SetTerminalError(http.StatusOK, "InternalExecutionError")
			recorder.recordedAmlfsConfigurations[amlFilesystemName] = amlFilesystem
			return resp, errResp
		}
		if nextFailureBehavior == eventualAscInternalErrorCreateFailureName {
			resp.SetTerminalError(http.StatusOK, "AscInternalError")
			recorder.recordedAmlfsConfigurations[amlFilesystemName] = amlFilesystem
			return resp, errResp
		}
		if nextFailureBehavior == eventualClusterCreateTimeoutFailureName {
			resp.SetTerminalError(http.StatusOK, "CreateTimeout")
			recorder.recordedAmlfsConfigurations[amlFilesystemName] = amlFilesystem
			return resp, errResp
		}

		recorder.recordedAmlfsConfigurations[amlFilesystemName] = amlFilesystem
		if recorder.createResponse != nil {
			amlFilesystem = *recorder.createResponse
		}
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
		if recorder.getError != nil {
			errResp.SetError(recorder.getError)
			return resp, errResp
		}
		for _, amlfs := range recorder.recordedAmlfsConfigurations {
			if *amlfs.Name == amlFilesystemName {
				amlFilesystem = &amlfs
			}
		}
		if amlFilesystem == nil {
			errResp.SetResponseError(http.StatusNotFound, "ResourceNotFound")
			return resp, errResp
		}
		nextFailureBehavior := getNextFailureBehavior(recorder)
		switch nextFailureBehavior {
		case clusterIsDeleting:
			amlFilesystem.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeDeleting)
		case clusterIsFailed:
			amlFilesystem.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeFailed)
		}

		resp.SetResponse(http.StatusOK,
			armstoragecache.AmlFilesystemsClientGetResponse{
				AmlFilesystem: *amlFilesystem,
			}, nil)
		return resp, errResp
	}
	return &fakeAmlfsServer
}

func TestUnusableSKUResponseFallsBackToARM(t *testing.T) {
	testCases := []struct {
		name    string
		change  func(*armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU
		message string
	}{
		{name: "nil SKU", change: func(_ *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			return nil
		}, message: "missing resource type"},
		{name: "missing resource type", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.ResourceType = nil
			return sku
		}, message: "missing resource type"},
		{name: "empty resource type", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.ResourceType = new("")
			return sku
		}, message: "missing resource type"},
		{name: "missing SKU name", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Name = nil
			return sku
		}, message: "missing SKU name"},
		{name: "empty SKU name", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Name = new("")
			return sku
		}, message: "missing SKU name"},
		{name: "missing location", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Locations[0] = nil
			return sku
		}, message: "missing location"},
		{name: "empty location", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Locations[0] = new("")
			return sku
		}, message: "missing location"},
		{name: "missing location info", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.LocationInfo[0] = nil
			return sku
		}, message: "missing location info"},
		{name: "missing zone in requested location", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.LocationInfo[0].Zones = []*string{nil}
			return sku
		}, message: "missing zone"},
		{name: "nil capability", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{nil}
			return sku
		}, message: "capacity increment or maximum"},
		{name: "missing capability name", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{{Value: new("4")}}
			return sku
		}, message: "capacity increment or maximum"},
		{name: "empty capability name", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{{Name: new(""), Value: new("4")}}
			return sku
		}, message: "capacity increment or maximum"},
		{name: "missing capacity increment", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{{Name: new(AmlfsSkuCapacityIncrementName)}}
			return sku
		}, message: "missing value for capability " + AmlfsSkuCapacityIncrementName},
		{name: "empty capacity increment", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{{Name: new(AmlfsSkuCapacityIncrementName), Value: new("")}}
			return sku
		}, message: "missing value for capability " + AmlfsSkuCapacityIncrementName},
		{name: "missing maximum capacity", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{{Name: new(AmlfsSkuCapacityMaximumName)}}
			return sku
		}, message: "missing value for capability " + AmlfsSkuCapacityMaximumName},
		{name: "empty maximum capacity", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{{Name: new(AmlfsSkuCapacityMaximumName), Value: new("")}}
			return sku
		}, message: "missing value for capability " + AmlfsSkuCapacityMaximumName},
		{name: "capacity capabilities absent", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Capabilities = nil
			return sku
		}, message: "capacity increment or maximum"},
		{name: "maximum capacity capability absent", change: func(sku *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			sku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{{Name: new(AmlfsSkuCapacityIncrementName), Value: new("4")}}
			return sku
		}, message: "capacity increment or maximum"},
		{name: "non-numeric increment", change: func(_ *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			return newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, "invalid", "128", expectedZones)
		}, message: AmlfsSkuCapacityIncrementName},
		{name: "non-numeric maximum", change: func(_ *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			return newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, "4", "invalid", expectedZones)
		}, message: AmlfsSkuCapacityMaximumName},
		{name: "zero increment", change: func(_ *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			return newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, "0", "128", expectedZones)
		}, message: AmlfsSkuCapacityIncrementName},
		{name: "negative maximum", change: func(_ *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			return newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, "4", "-1", expectedZones)
		}, message: AmlfsSkuCapacityMaximumName},
		{name: "integer overflow", change: func(_ *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			return newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, "4", "9223372036854775808", expectedZones)
		}, message: AmlfsSkuCapacityMaximumName},
		{name: "capacity overflows CSI byte range", change: func(_ *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			return newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, "4", "8388608", expectedZones)
		}, message: AmlfsSkuCapacityMaximumName},
		{name: "increment exceeds maximum", change: func(_ *armstoragecache.ResourceSKU) *armstoragecache.ResourceSKU {
			return newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, "256", "128", expectedZones)
		}, message: "capacity increment or maximum"},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			sku := newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, expectedSkuIncrement, expectedSkuMaximum, expectedZones)
			recorder := newMockAmlfsRecorder(nil)
			recorder.skuResponse = &armstoragecache.ResourceSKUsResult{Value: []*armstoragecache.ResourceSKU{
				newResourceSku(AmlfsSkuResourceType, otherSkuForLocation, expectedLocation, expectedSkuIncrement, expectedSkuMaximum, expectedZones),
				test.change(sku),
			}}
			provisioner := newTestDynamicProvisioner(t, recorder)

			skuValues, err := provisioner.GetSkuValuesForLocation(t.Context(), expectedLocation)

			require.Error(t, err)
			assert.Equal(t, codes.Unavailable, status.Code(err), "unusable metadata must not be returned as a successful partial catalog")
			require.ErrorContains(t, err, test.message)
			require.ErrorContains(t, err, expectedLocation)
			assert.NotContains(t, skuValues, expectedSku)
			assert.Contains(t, skuValues, otherSkuForLocation, "a malformed requested SKU must not discard another usable entry")
			assert.Empty(t, recorder.recordedAmlfsConfigurations)
			assert.Equal(t, []string{"SKUsServerTransport.NewListPager"}, recorder.fakeCallCount)

			recorder.subnetUsageResponse = &armnetwork.VirtualNetworkListUsageResult{Value: []*armnetwork.VirtualNetworkUsage{
				{
					ID:           new("/subscriptions/fake-subscription-id/resourceGroups/fake-resource-group/providers/Microsoft.Network/virtualNetworks/fake-vnet/subnets/fake-subnet-name"),
					CurrentValue: new(float64(expectedUsedIPCount)),
					Limit:        new(float64(expectedTotalIPCount)),
				},
			}}
			driver := NewFakeDriver(t)
			driver.cloud.SubscriptionID = "fake-subscription-id"
			driver.dynamicProvisioner = provisioner
			request := buildDynamicProvCreateVolumeRequest()
			request.Name = expectedAmlFilesystemName
			request.Parameters[VolumeContextSkuName] = expectedSku
			request.Parameters[VolumeContextLocation] = expectedLocation
			request.Parameters[VolumeContextResourceGroupName] = expectedResourceGroupName
			request.Parameters[VolumeContextVnetResourceGroup] = expectedResourceGroupName
			request.Parameters[VolumeContextVnetName] = expectedVnetName
			request.Parameters[VolumeContextSubnetName] = expectedAmlFilesystemSubnetName
			request.CapacityRange = &csi.CapacityRange{RequiredBytes: 9 * util.TiB, LimitBytes: 12 * util.TiB}

			response, err := driver.CreateVolume(t.Context(), request)

			require.NoError(t, err, "unusable SKU metadata must not block the creation attempt")
			assert.Equal(t, int64(12*util.TiB), response.GetVolume().GetCapacityBytes())
			require.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName)
			created := recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName]
			require.NotNil(t, created.SKU)
			assert.Equal(t, new(expectedSku), created.SKU.Name)
			assert.Equal(t, []*string{new("zone1")}, created.Zones)
			require.NotNil(t, created.Properties)
			assert.Equal(t, new(float32(12)), created.Properties.StorageCapacityTiB)
			assert.Contains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginCreateOrUpdate")
			assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginDelete")

			recorder.skuResponse = &armstoragecache.ResourceSKUsResult{Value: []*armstoragecache.ResourceSKU{
				newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, expectedSkuIncrement, expectedSkuMaximum, expectedZones),
			}}
			recorder.fakeCallCount = nil

			skuValues, err = provisioner.GetSkuValuesForLocation(t.Context(), expectedLocation)

			require.NoError(t, err, "a subsequent complete response must succeed")
			assert.Contains(t, skuValues, expectedSku)
			assert.Equal(t, []string{"SKUsServerTransport.NewListPager"}, recorder.fakeCallCount)
		})
	}
}

func TestCreateVolumeUsesValidRequestedSKUDespiteOtherMalformedData(t *testing.T) {
	for _, test := range []struct {
		name          string
		change        func(*armstoragecache.ResourceSKU)
		requiredTiB   int64
		zone          string
		expectedCode  codes.Code
		expectedError string
	}{
		{name: "another SKU has invalid capacity", requiredTiB: 9, zone: "zone1"},
		{name: "unused requested capability fields are malformed", requiredTiB: 9, zone: "zone1", change: func(sku *armstoragecache.ResourceSKU) {
			sku.Capabilities = append(sku.Capabilities, nil,
				&armstoragecache.ResourceSKUCapabilities{Value: new("unused")},
				&armstoragecache.ResourceSKUCapabilities{Name: new("unused capability")})
		}},
		{name: "unrelated location fields are missing", requiredTiB: 9, zone: "zone1", change: func(sku *armstoragecache.ResourceSKU) {
			sku.Locations = append([]*string{nil}, sku.Locations...)
			sku.LocationInfo = append(sku.LocationInfo, nil,
				&armstoragecache.ResourceSKULocationInfo{Location: new("other-location")})
		}},
		{name: "known maximum still applies", requiredTiB: 72, zone: "zone1", expectedCode: codes.InvalidArgument, expectedError: "exceeds maximum capacity"},
		{name: "known zones still apply", requiredTiB: 9, zone: "zone2", expectedCode: codes.InvalidArgument, expectedError: "must be one of"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestedSKU := newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, "8", "64", []string{"zone1"})
			if test.change != nil {
				test.change(requestedSKU)
			}
			recorder := newMockAmlfsRecorder(nil)
			recorder.skuResponse = &armstoragecache.ResourceSKUsResult{Value: []*armstoragecache.ResourceSKU{
				nil,
				newResourceSku(AmlfsSkuResourceType, otherSkuForLocation, expectedLocation, "invalid", "128", expectedZones),
				requestedSKU,
			}}
			recorder.subnetUsageResponse = &armnetwork.VirtualNetworkListUsageResult{Value: []*armnetwork.VirtualNetworkUsage{
				{
					ID:           new("/subscriptions/fake-subscription-id/resourceGroups/fake-resource-group/providers/Microsoft.Network/virtualNetworks/fake-vnet/subnets/fake-subnet-name"),
					CurrentValue: new(float64(expectedUsedIPCount)),
					Limit:        new(float64(expectedTotalIPCount)),
				},
			}}
			driver := NewFakeDriver(t)
			driver.cloud.SubscriptionID = "fake-subscription-id"
			driver.dynamicProvisioner = newTestDynamicProvisioner(t, recorder)
			request := buildDynamicProvCreateVolumeRequest()
			request.Name = expectedAmlFilesystemName
			request.Parameters[VolumeContextSkuName] = expectedSku
			request.Parameters[VolumeContextLocation] = expectedLocation
			request.Parameters[VolumeContextResourceGroupName] = expectedResourceGroupName
			request.Parameters[VolumeContextVnetResourceGroup] = expectedResourceGroupName
			request.Parameters[VolumeContextVnetName] = expectedVnetName
			request.Parameters[VolumeContextSubnetName] = expectedAmlFilesystemSubnetName
			request.Parameters[VolumeContextZone] = test.zone
			request.CapacityRange = &csi.CapacityRange{RequiredBytes: test.requiredTiB * util.TiB}

			response, err := driver.CreateVolume(t.Context(), request)

			if test.expectedCode != codes.OK {
				require.ErrorContains(t, err, test.expectedError)
				assert.Equal(t, test.expectedCode, status.Code(err))
				assert.Nil(t, response)
				assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginCreateOrUpdate")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, int64(16*util.TiB), response.GetVolume().GetCapacityBytes(),
				"the usable eight-TiB SKU increment must not be replaced by the four-TiB fallback")
			require.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName)
			created := recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName]
			require.NotNil(t, created.Properties)
			assert.Equal(t, new(float32(16)), created.Properties.StorageCapacityTiB)
			assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginDelete")
		})
	}
}

func TestDynamicProvisioner_GetSkuValuesForLocation_IgnoresUnusedCapabilityValues(t *testing.T) {
	sku := newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, expectedSkuIncrement, expectedSkuMaximum, expectedZones)
	sku.Capabilities = []*armstoragecache.ResourceSKUCapabilities{
		{Name: new(AmlfsSkuCapacityIncrementName), Value: new(expectedSkuIncrement)},
		{Name: new(AmlfsSkuCapacityMaximumName), Value: new(expectedSkuMaximum)},
		{Name: new("bandwidth increment (MB/s/TiB)"), Value: nil},
	}
	recorder := newMockAmlfsRecorder(nil)
	recorder.skuResponse = &armstoragecache.ResourceSKUsResult{Value: []*armstoragecache.ResourceSKU{sku}}
	provisioner := newTestDynamicProvisioner(t, recorder)

	skuValues, err := provisioner.GetSkuValuesForLocation(t.Context(), expectedLocation)

	require.NoError(t, err, "a missing unused bandwidth value must not discard valid capacity metadata")
	require.Contains(t, skuValues, expectedSku)
	assert.Equal(t, int64(4), skuValues[expectedSku].IncrementInTib)
	assert.Equal(t, int64(128), skuValues[expectedSku].MaximumInTib)
	assert.ElementsMatch(t, expectedZones, skuValues[expectedSku].AvailableZones,
		"ignoring an unused capability must preserve the advertised zones")
}

func TestDynamicProvisioner_GetSkuValuesForLocation_FiltersUnrelatedEntriesBeforeValidation(t *testing.T) {
	testCases := []struct {
		name  string
		entry *armstoragecache.ResourceSKU
	}{
		{name: "another resource type may omit AMLFS fields", entry: &armstoragecache.ResourceSKU{ResourceType: new("caches")}},
		{
			name: "another location may have unusable AMLFS metadata",
			entry: &armstoragecache.ResourceSKU{
				ResourceType: new(AmlfsSkuResourceType),
				Locations:    []*string{new("other-location")},
				LocationInfo: []*armstoragecache.ResourceSKULocationInfo{nil},
				Capabilities: []*armstoragecache.ResourceSKUCapabilities{nil},
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			recorder.skuResponse = &armstoragecache.ResourceSKUsResult{Value: []*armstoragecache.ResourceSKU{
				testCase.entry,
				newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, expectedSkuIncrement, expectedSkuMaximum, expectedZones),
			}}

			skuValues, err := newTestDynamicProvisioner(t, recorder).GetSkuValuesForLocation(t.Context(), expectedLocation)

			require.NoError(t, err, "unrelated incomplete metadata must not reject the requested location's SKU")
			require.Len(t, skuValues, 1)
			require.Contains(t, skuValues, expectedSku)
			assert.Equal(t, int64(4), skuValues[expectedSku].IncrementInTib)
			assert.Equal(t, int64(128), skuValues[expectedSku].MaximumInTib)
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_IncompleteCreateReplyRecoversByReadyLookup(t *testing.T) {
	testCases := []struct {
		name    string
		mutate  func(*armstoragecache.AmlFilesystem)
		message string
	}{
		{name: "missing resource ID", mutate: func(f *armstoragecache.AmlFilesystem) {
			f.ID = nil
		}, message: "missing resource ID"},
		{name: "wrong resource ID", mutate: func(f *armstoragecache.AmlFilesystem) {
			*f = readyTestAmlFilesystem(expectedAmlFilesystemName + "-other")
		}, message: "unexpected resource ID"},
		{name: "missing properties", mutate: func(f *armstoragecache.AmlFilesystem) {
			f.Properties = nil
		}, message: "missing provisioning state"},
		{name: "missing client info", mutate: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.ClientInfo = nil
		}, message: "missing MGS address"},
		{name: "missing MGS address", mutate: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.ClientInfo.MgsAddress = nil
		}, message: "missing MGS address"},
		{name: "empty MGS address", mutate: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.ClientInfo.MgsAddress = new("")
		}, message: "missing MGS address"},
		{name: "blank MGS address", mutate: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.ClientInfo.MgsAddress = new(" \t\n")
		}, message: "missing MGS address"},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			response := readyTestAmlFilesystem(expectedAmlFilesystemName)
			test.mutate(&response)
			recorder.createResponse = &response
			provisioner := newTestDynamicProvisioner(t, recorder)
			properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)

			result, err := provisioner.CreateAmlFilesystem(t.Context(), properties)

			require.Error(t, err)
			assert.Equal(t, codes.Unavailable, status.Code(err), "the provisioner must retain the unfinished attempt even if its PVC was deleted")
			require.ErrorContains(t, err, test.message)
			require.ErrorContains(t, err, expectedAmlFilesystemName)
			require.ErrorContains(t, err, expectedResourceGroupName)
			assert.Nil(t, result)
			assert.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName, "an incomplete response must not delete the created resource")
			stored := recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName]
			require.Equal(t, new(armstoragecache.AmlFilesystemProvisioningStateTypeSucceeded), stored.Properties.ProvisioningState,
				"only the create reply is malformed; the recorded backend completed creation")
			assert.Equal(t, []string{
				"AmlFilesystemsServerTransport.Get",
				"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
				"VirtualNetworksServerTransport.NewListUsagePager",
				"AmlFilesystemsServerTransport.BeginCreateOrUpdate",
			}, recorder.fakeCallCount, "no destructive retry after a malformed successful response")

			recorder.fakeCallCount = nil

			result, err = provisioner.CreateAmlFilesystem(t.Context(), properties)

			require.NoError(t, err, "a subsequent complete response must succeed")
			assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: expectedClusterSize * util.TiB}, result)
			assert.Equal(t, []string{
				"AmlFilesystemsServerTransport.Get",
			}, recorder.fakeCallCount, "the retry must reuse the completed resource without replaying the PUT")
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_IncompleteSubnetUsage(t *testing.T) {
	testCases := []struct {
		name    string
		usage   *armnetwork.VirtualNetworkUsage
		message string
	}{
		{name: "nil usage entry", message: "missing subnet ID"},
		{name: "missing subnet ID", usage: &armnetwork.VirtualNetworkUsage{}, message: "missing subnet ID"},
		{name: "empty subnet ID", usage: &armnetwork.VirtualNetworkUsage{ID: new("")}, message: "missing subnet ID"},
		{name: "missing current value", usage: &armnetwork.VirtualNetworkUsage{
			ID: new(expectedAmlFilesystemSubnetID), Limit: new(float64(expectedTotalIPCount)),
		}, message: "missing CurrentValue or Limit"},
		{name: "missing limit", usage: &armnetwork.VirtualNetworkUsage{
			ID: new(expectedAmlFilesystemSubnetID), CurrentValue: new(float64(expectedUsedIPCount)),
		}, message: "missing CurrentValue or Limit"},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			recorder.subnetUsageResponse = &armnetwork.VirtualNetworkListUsageResult{
				Value: []*armnetwork.VirtualNetworkUsage{
					{ID: new("unrelated-subnet")},
					test.usage,
				},
			}
			provisioner := newTestDynamicProvisioner(t, recorder)
			properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)

			result, err := provisioner.CreateAmlFilesystem(t.Context(), properties)

			require.Error(t, err)
			assert.Equal(t, codes.Unavailable, status.Code(err), "incomplete subnet observations must remain retryable")
			require.ErrorContains(t, err, test.message)
			require.ErrorContains(t, err, expectedVnetName)
			require.ErrorContains(t, err, expectedResourceGroupName)
			assert.Nil(t, result)
			assert.Empty(t, recorder.recordedAmlfsConfigurations)
			assert.Equal(t, []string{
				"AmlFilesystemsServerTransport.Get",
				"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
				"VirtualNetworksServerTransport.NewListUsagePager",
			}, recorder.fakeCallCount, "invalid capacity data must not create or delete a filesystem")

			recorder.subnetUsageResponse = &armnetwork.VirtualNetworkListUsageResult{Value: []*armnetwork.VirtualNetworkUsage{{
				ID:           new(expectedAmlFilesystemSubnetID),
				CurrentValue: new(float64(0)),
				Limit:        new(float64(expectedTotalIPCount)),
			}}}
			recorder.fakeCallCount = nil

			result, err = provisioner.CreateAmlFilesystem(t.Context(), properties)

			require.NoError(t, err, "a complete retry with zero used addresses must be admitted")
			assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: expectedClusterSize * util.TiB}, result)
			assert.Equal(t, []string{
				"AmlFilesystemsServerTransport.Get",
				"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
				"VirtualNetworksServerTransport.NewListUsagePager",
				"AmlFilesystemsServerTransport.BeginCreateOrUpdate",
			}, recorder.fakeCallCount)
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_Success(t *testing.T) {
	expectedLocation := "fake-location"
	expectedMaintenanceDayOfWeek := armstoragecache.MaintenanceDayOfWeekTypeSaturday
	expectedTimeOfDayUTC := "12:00"
	expectedStorageCapacityTiB := float32(48)
	expectedZone := "zone1"

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	result, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), &AmlFilesystemProperties{
		ResourceGroupName:    expectedResourceGroupName,
		AmlFilesystemName:    expectedAmlFilesystemName,
		Location:             expectedLocation,
		MaintenanceDayOfWeek: expectedMaintenanceDayOfWeek,
		TimeOfDayUTC:         expectedTimeOfDayUTC,
		SKUName:              expectedSku,
		StorageCapacityTiB:   expectedStorageCapacityTiB,
		SubnetInfo:           buildExpectedSubnetInfo(),
		Zone:                 expectedZone,
	})
	require.NoError(t, err)
	assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: expectedClusterSize * util.TiB}, result)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	actualAmlFilesystem := recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName]
	assert.Equal(t, expectedLocation, *actualAmlFilesystem.Location)
	assert.Equal(t, expectedAmlFilesystemSubnetID, *actualAmlFilesystem.Properties.FilesystemSubnet)
	require.NotNil(t, actualAmlFilesystem.Properties.MaintenanceWindow)
	assert.Equal(t, new(expectedMaintenanceDayOfWeek), actualAmlFilesystem.Properties.MaintenanceWindow.DayOfWeek)
	assert.Equal(t, new(expectedTimeOfDayUTC), actualAmlFilesystem.Properties.MaintenanceWindow.TimeOfDayUTC)
	assert.Equal(t, expectedSku, *recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].SKU.Name)
	assert.Nil(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Identity)
	require.Len(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Zones, 1)
	assert.Equal(t, expectedZone, *recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Zones[0])
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

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
	properties.Tags = expectedTags
	_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), properties)
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	assert.Len(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Tags, len(expectedTags))
	for tagName, tagValue := range expectedTags {
		assert.Equal(t, new(tagValue), recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Tags[tagName],
			"the Azure request must include the requested value for tag %q", tagName)
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_OmitsUnspecifiedZoneFromAzureRequest(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
	properties.Zone = ""
	_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), properties)
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	require.Nil(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Zones, "Expected no zones to be set when zone is empty")
}

func TestDynamicProvisioner_CreateAmlFilesystem_Success_Identities(t *testing.T) {
	expectedIdentities := []string{"identity1", "identity2"}

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
	properties.Identities = expectedIdentities
	_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), properties)
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	require.NotNil(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Identity)
	assert.Equal(t, armstoragecache.AmlFilesystemIdentityTypeUserAssigned, *recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Identity.Type)
	assert.Len(t, recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Identity.UserAssignedIdentities, len(expectedIdentities))
	for identityKey, identityValue := range recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName].Identity.UserAssignedIdentities {
		assert.Equal(t, &armstoragecache.UserAssignedIdentitiesValue{}, identityValue)
		assert.Contains(t, expectedIdentities, identityKey)
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_ErrorMessagesNeverAuthorizeDeletion(t *testing.T) {
	expectedCreateCalls := []string{
		"AmlFilesystemsServerTransport.Get",
		"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
		"VirtualNetworksServerTransport.NewListUsagePager",
		"AmlFilesystemsServerTransport.BeginCreateOrUpdate",
	}
	testCases := []struct {
		name                    string
		failureBehavior         string
		expectedError           string
		backendExistsAfterReply bool
	}{
		{
			name:            "immediate timeout",
			failureBehavior: immediateClusterCreateTimeoutFailureName,
			expectedError:   "CreateTimeout",
		},
		{
			name:                    "polling timeout",
			failureBehavior:         eventualClusterCreateTimeoutFailureName,
			expectedError:           "CreateTimeout",
			backendExistsAfterReply: true,
		},
		{
			name:                    "immediate internal error",
			failureBehavior:         immediateInternalExecutionCreateFailureName,
			expectedError:           "InternalExecutionError",
			backendExistsAfterReply: true,
		},
		{
			name:                    "polling internal error",
			failureBehavior:         eventualInternalExecutionCreateFailureName,
			expectedError:           "InternalExecutionError",
			backendExistsAfterReply: true,
		},
		{
			name:                    "immediate ASC error",
			failureBehavior:         immediateAscInternalErrorCreateFailureName,
			expectedError:           "AscInternalError",
			backendExistsAfterReply: true,
		},
		{
			name:                    "polling ASC error",
			failureBehavior:         eventualAscInternalErrorCreateFailureName,
			expectedError:           "AscInternalError",
			backendExistsAfterReply: true,
		},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder([]string{test.failureBehavior})
			dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

			result, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
			require.ErrorContains(t, err, expectedAmlFilesystemName)
			require.ErrorContains(t, err, test.expectedError)
			assert.Equal(t, expectedCreateCalls, recorder.fakeCallCount, "an error reply alone must not trigger deletion; retry must observe the resource")
			if test.backendExistsAfterReply {
				assert.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName, "uncertain accepted work must not be deleted")
			} else {
				assert.Empty(t, recorder.recordedAmlfsConfigurations)
			}
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_Timeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := newMockAmlfsRecorder([]string{})
		dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
		// Use 1.5x the poll frequency so the deadline expires between poll ticks
		// rather than coinciding with one. Aligning with a tick creates a select
		// race between time.After and ctx.Done in the SDK's Delay helper.
		ctx, cancel := context.WithTimeout(t.Context(), 3*quickPollFrequency/2)
		defer cancel()

		_, err := dynamicProvisioner.CreateAmlFilesystem(ctx, buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))
		require.Error(t, err)
		grpcStatus, ok := status.FromError(err)
		require.True(t, ok, "error should be a gRPC status, got: %v", err)
		assert.Equal(t, codes.DeadlineExceeded, grpcStatus.Code())
		require.ErrorContains(t, err, "context deadline exceeded")
		assert.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName)
		assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginDelete")
		recorder.fakeCallCount = nil

		result, err := newTestDynamicProvisioner(t, recorder).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

		require.NoError(t, err, "a restarted controller must observe accepted work, not repeat creation")
		assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: expectedClusterSize * util.TiB}, result)
		assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)
	})
}

func TestDynamicProvisioner_CreateAmlFilesystem_RetryMustObserveAfterUncertainCreateError(t *testing.T) {
	testCases := []struct {
		desc            string
		failureBehavior string
	}{
		{
			desc:            "Immediate Internal Execution create failure",
			failureBehavior: immediateInternalExecutionCreateFailureName,
		},
		{
			desc:            "Eventual Internal Execution create failure",
			failureBehavior: eventualInternalExecutionCreateFailureName,
		},
		{
			desc:            "Immediate ASC Internal Error create failure",
			failureBehavior: immediateAscInternalErrorCreateFailureName,
		},
		{
			desc:            "Eventual ASC Internal Error create failure",
			failureBehavior: eventualAscInternalErrorCreateFailureName,
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			failureBehaviors := []string{
				tC.failureBehavior,
			}
			recorder := newMockAmlfsRecorder(failureBehaviors)
			dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

			properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
			_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), properties)
			require.Error(t, err)
			assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
			assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginDelete")

			recorder.getError = &azcore.ResponseError{StatusCode: http.StatusInternalServerError, ErrorCode: "RetryLookupFailed"}
			_, err = dynamicProvisioner.CreateAmlFilesystem(t.Context(), properties)
			require.Error(t, err)
			grpcStatus, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, codes.Unavailable, grpcStatus.Code())
			require.ErrorContains(t, err, "RetryLookupFailed")
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
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_NilClient(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	dynamicProvisioner.amlFilesystemsClient = nil
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        SubnetProperties{},
	})
	require.ErrorContains(t, err, "aml filesystem client is nil")
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_CreateAmlFilesystem_ReportsTheFailingAzureStage(t *testing.T) {
	testCases := []struct {
		name            string
		subnetUsage     *armnetwork.VirtualNetworkListUsageResult
		submitError     error
		pollError       *azcore.ResponseError
		expectedCode    codes.Code
		expectedMessage string
		submitted       bool
	}{
		{
			name:            "admission cannot find the requested subnet",
			subnetUsage:     &armnetwork.VirtualNetworkListUsageResult{Value: []*armnetwork.VirtualNetworkUsage{}},
			expectedCode:    codes.FailedPrecondition,
			expectedMessage: "not found in vnet",
		},
		{
			name:            "PUT submission returns BadGateway",
			submitError:     &azcore.ResponseError{StatusCode: http.StatusBadGateway, ErrorCode: "PutRejected"},
			expectedCode:    codes.Unavailable,
			expectedMessage: "PutRejected",
			submitted:       true,
		},
		{
			name:            "PUT submission returns HTTP RequestTimeout",
			submitError:     &azcore.ResponseError{StatusCode: http.StatusRequestTimeout, ErrorCode: "RequestTimeout"},
			expectedCode:    codes.DeadlineExceeded,
			expectedMessage: "RequestTimeout",
			submitted:       true,
		},
		{
			name:            "fresh PUT definitively rejects the SKU",
			submitError:     &azcore.ResponseError{StatusCode: http.StatusBadRequest, ErrorCode: "SkuNotAvailable"},
			expectedCode:    codes.InvalidArgument,
			expectedMessage: "SkuNotAvailable",
			submitted:       true,
		},
		{
			name:            "fresh PUT definitively rejects authentication",
			submitError:     &azcore.ResponseError{StatusCode: http.StatusUnauthorized, ErrorCode: "AuthenticationFailed"},
			expectedCode:    codes.Unauthenticated,
			expectedMessage: "AuthenticationFailed",
			submitted:       true,
		},
		{
			name:            "fresh PUT definitively denies creation permission",
			submitError:     &azcore.ResponseError{StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed"},
			expectedCode:    codes.PermissionDenied,
			expectedMessage: "AuthorizationFailed",
			submitted:       true,
		},
		{
			name:            "accepted create poll loses authentication",
			pollError:       &azcore.ResponseError{StatusCode: http.StatusUnauthorized, ErrorCode: "AuthenticationFailed"},
			expectedCode:    codes.Unavailable,
			expectedMessage: "AuthenticationFailed",
			submitted:       true,
		},
		{
			name:            "accepted create poll loses permission",
			pollError:       &azcore.ResponseError{StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed"},
			expectedCode:    codes.Unavailable,
			expectedMessage: "AuthorizationFailed",
			submitted:       true,
		},
		{
			name:            "accepted create poll reports a conflict",
			pollError:       &azcore.ResponseError{StatusCode: http.StatusConflict, ErrorCode: "OperationInProgress"},
			expectedCode:    codes.Unavailable,
			expectedMessage: "OperationInProgress",
			submitted:       true,
		},
		{
			name:            "accepted create reports a terminal service rejection that still needs observation",
			pollError:       &azcore.ResponseError{StatusCode: http.StatusBadRequest, ErrorCode: "AllocationRejected"},
			expectedCode:    codes.Unavailable,
			expectedMessage: "AllocationRejected",
			submitted:       true,
		},
		{
			name:            "operation poll returns HTTP RequestTimeout",
			pollError:       &azcore.ResponseError{StatusCode: http.StatusRequestTimeout, ErrorCode: "PollTimedOut"},
			expectedCode:    codes.DeadlineExceeded,
			expectedMessage: "PollTimedOut",
			submitted:       true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			recorder.subnetUsageResponse = testCase.subnetUsage
			recorder.createError = testCase.submitError
			recorder.createPollingError = testCase.pollError
			provisioner := newTestDynamicProvisioner(t, recorder)

			result, err := provisioner.CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

			assert.Nil(t, result)
			require.ErrorContains(t, err, testCase.expectedMessage)
			assert.Equal(t, testCase.expectedCode, status.Code(err), "the failing stage must report %s; got %v", testCase.expectedCode, err)
			if testCase.submitted {
				assert.Contains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginCreateOrUpdate")
			} else {
				assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginCreateOrUpdate", "failed admission must prevent PUT")
			}
			assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginDelete", "a failed request is not deletion authority")
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_Err_EmptySubnetInfo(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), &AmlFilesystemProperties{
		ResourceGroupName: expectedResourceGroupName,
		AmlFilesystemName: expectedAmlFilesystemName,
		SubnetInfo:        SubnetProperties{},
	})
	require.ErrorContains(t, err, "invalid subnet info")
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_CreateAmlFilesystem_InsufficientSubnetCapacityPreventsCreation(t *testing.T) {
	recorder := newMockAmlfsRecorder(nil)
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
	properties.SubnetInfo.VnetName = fullVnetName

	result, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), properties)

	require.ErrorContains(t, err, properties.SubnetInfo.SubnetID)
	require.ErrorContains(t, err, "not enough IP addresses available")
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	assert.Nil(t, result)
	assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginCreateOrUpdate",
		"insufficient subnet capacity must stop creation before a PUT")
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_CreateAmlFilesystem_ReadyRetrySkipsSubnetAdmission(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	subnetProperties := buildExpectedSubnetInfo()

	properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
	properties.SubnetInfo = subnetProperties
	_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), properties)
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)

	currentFilesystem, err := dynamicProvisioner.GetAmlFilesystem(t.Context(), properties)
	require.NoError(t, err)
	require.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: expectedClusterSize * util.TiB}, currentFilesystem)

	recorder.subnetUsageResponse = &armnetwork.VirtualNetworkListUsageResult{Value: []*armnetwork.VirtualNetworkUsage{{
		ID:           new(expectedAmlFilesystemSubnetID),
		CurrentValue: new(float64(expectedTotalIPCount)),
		Limit:        new(float64(expectedTotalIPCount)),
	}}}

	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	recorder.fakeCallCount = nil
	reused, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), properties)
	require.NoError(t, err)
	assert.Equal(t, currentFilesystem, reused)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount, "reuse must skip capacity checks and all writes")
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Success(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)

	err = dynamicProvisioner.DeleteAmlFilesystem(t.Context(), expectedResourceGroupName, expectedAmlFilesystemName)

	require.NoError(t, err)
	assert.Empty(t, recorder.recordedAmlfsConfigurations)

	err = dynamicProvisioner.DeleteAmlFilesystem(t.Context(), expectedResourceGroupName, expectedAmlFilesystemName)

	require.NoError(t, err, "repeating deletion after completion must succeed")
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_DeleteAmlFilesystem_AbsentBackendCompletesWithoutPolling(t *testing.T) {
	recorder := newMockAmlfsRecorder(nil)
	provisioner := newTestDynamicProvisioner(t, recorder)

	err := provisioner.DeleteAmlFilesystem(t.Context(), expectedResourceGroupName, expectedAmlFilesystemName)

	require.NoError(t, err, "the documented HTTP 204 response must succeed without polling")
	assert.Equal(t, []string{"AmlFilesystemsServerTransport.BeginDelete"}, recorder.fakeCallCount)
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_DeleteAmlFilesystem_RejectsSubmissionErrors(t *testing.T) {
	testCases := []struct {
		name         string
		response     error
		expectedCode codes.Code
	}{
		{name: "resource not found error is not the documented 204", response: &azcore.ResponseError{
			StatusCode: http.StatusNotFound, ErrorCode: "ResourceNotFound",
		}, expectedCode: codes.NotFound},
		{name: "missing resource group", response: &azcore.ResponseError{
			StatusCode: http.StatusNotFound, ErrorCode: "ResourceGroupNotFound",
		}, expectedCode: codes.NotFound},
		{name: "missing operation", response: &azcore.ResponseError{
			StatusCode: http.StatusNotFound, ErrorCode: "OperationNotFound",
		}, expectedCode: codes.NotFound},
		{name: "permission denied", response: &azcore.ResponseError{
			StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed",
		}, expectedCode: codes.PermissionDenied},
		{name: "unauthenticated", response: &azcore.ResponseError{
			StatusCode: http.StatusUnauthorized, ErrorCode: "AuthenticationFailed",
		}, expectedCode: codes.Unauthenticated},
		{name: "throttled", response: &azcore.ResponseError{
			StatusCode: http.StatusTooManyRequests, ErrorCode: "TooManyRequests",
		}, expectedCode: codes.Unavailable},
		{name: "untyped error text", response: errors.New("ResourceNotFound"), expectedCode: codes.Unknown},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			recorder.deleteError = test.response
			provisioner := newTestDynamicProvisioner(t, recorder)

			err := provisioner.DeleteAmlFilesystem(t.Context(), expectedResourceGroupName, expectedAmlFilesystemName)

			require.Error(t, err)
			assert.Equal(t, test.expectedCode, status.Code(err),
				"DELETE submission must report %s instead of claiming deletion completed; got %v", test.expectedCode, err)
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.BeginDelete"}, recorder.fakeCallCount)
		})
	}
}

type amlfsARMExchange struct {
	method, path string
	status       int
	body         string
	headers      http.Header
	err          error
}

type amlfsARMScenario struct {
	t         *testing.T
	exchanges []amlfsARMExchange
	calls     []string
}

func (s *amlfsARMScenario) Do(request *http.Request) (*http.Response, error) {
	s.t.Helper()
	index := len(s.calls)
	s.calls = append(s.calls, request.Method+" "+request.URL.Path)
	require.Less(s.t, index, len(s.exchanges), "unexpected ARM request: %s %s", request.Method, request.URL.Path)
	exchange := s.exchanges[index]
	require.Equal(s.t, exchange.method, request.Method, "ARM exchange %d must use the scripted method; calls so far: %v", index+1, s.calls)
	require.Equal(s.t, exchange.path, request.URL.Path, "ARM exchange %d must target the scripted resource/operation", index+1)
	if exchange.err != nil {
		return nil, exchange.err
	}
	headers := exchange.headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: exchange.status,
		Header:     headers,
		Request:    request,
		Body:       io.NopCloser(strings.NewReader(exchange.body)),
	}, nil
}

func (s *amlfsARMScenario) provisioner(t *testing.T) *DynamicProvisioner {
	t.Helper()
	client, err := armstoragecache.NewAmlFilesystemsClient(expectedSubscriptionID, &azfake.TokenCredential{}, &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: s,
			Retry:     policy.RetryOptions{MaxRetries: -1},
		},
	})
	require.NoError(t, err)
	provisioner := newTestDynamicProvisioner(t, newMockAmlfsRecorder(nil))
	provisioner.amlFilesystemsClient = client
	return provisioner
}

func newAmlfsARMScenario(t *testing.T, exchanges []amlfsARMExchange) *amlfsARMScenario {
	t.Helper()
	scenario := &amlfsARMScenario{t: t, exchanges: exchanges}
	t.Cleanup(func() {
		assert.Len(t, scenario.calls, len(scenario.exchanges), "the expected ARM lifecycle must actually be exercised")
	})
	return scenario
}

func amlfsTestJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return string(body)
}

func amlfsTestResourceID() string {
	return amlFilesystemResourceID(expectedSubscriptionID, expectedResourceGroupName, expectedAmlFilesystemName)
}

const (
	amlfsTestCreateOperation = "/subscriptions/fake-subscription-id/providers/Microsoft.StorageCache/locations/fake-location/ascOperations/create-operation"
	amlfsTestDeleteOperation = "/subscriptions/fake-subscription-id/providers/Microsoft.StorageCache/locations/fake-location/ascOperations/delete-operation"
)

func TestDynamicProvisioner_CreateAmlFilesystem_FailedCreateRecoversAfterRestartAndDeletion(t *testing.T) {
	creating := readyTestAmlFilesystem(expectedAmlFilesystemName)
	creating.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeCreating)
	failed := readyTestAmlFilesystem(expectedAmlFilesystemName)
	failed.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeFailed)
	failed.Properties.ClientInfo = nil
	failed.Properties.CurrentStorageCapacityTiB = nil
	steps := []amlfsARMExchange{
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
		{
			method: http.MethodPut, path: amlfsTestResourceID(), status: http.StatusCreated, body: amlfsTestJSON(t, creating),
			headers: http.Header{"Azure-Asyncoperation": []string{"https://management.azure.com" + amlfsTestCreateOperation}},
		},
		{method: http.MethodGet, path: amlfsTestCreateOperation, status: http.StatusOK, body: `{"status":"Failed","error":{"code":"AllocationRejected"}}`},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, failed)},
		{
			method: http.MethodDelete, path: amlfsTestResourceID(), status: http.StatusAccepted,
			headers: http.Header{"Azure-Asyncoperation": []string{"https://management.azure.com" + amlfsTestDeleteOperation}},
		},
		{method: http.MethodGet, path: amlfsTestDeleteOperation, status: http.StatusOK, body: `{"status":"InProgress"}`},
		{method: http.MethodGet, path: amlfsTestDeleteOperation, status: http.StatusOK, body: `{"status":"Succeeded"}`},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
		{method: http.MethodPut, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, readyTestAmlFilesystem(expectedAmlFilesystemName))},
	}
	scenario := newAmlfsARMScenario(t, steps)
	provisioner := scenario.provisioner(t)
	properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)

	result, err := provisioner.CreateAmlFilesystem(t.Context(), properties)

	assert.Nil(t, result)
	require.Error(t, err)
	assert.Len(t, scenario.calls, 3, "a failed operation response is observed again on the next CSI retry")

	restarted := scenario.provisioner(t)
	result, err = restarted.CreateAmlFilesystem(t.Context(), properties)

	assert.Nil(t, result)
	assert.Equal(t, codes.Aborted, status.Code(err))
	require.ErrorContains(t, err, "deletion completed, retry creation")
	assert.Len(t, scenario.calls, 7, "the failed initial resource must be deleted to completion before retry")

	result, err = restarted.CreateAmlFilesystem(t.Context(), properties)

	require.NoError(t, err)
	assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: expectedClusterSize * util.TiB}, result)
}

func TestDynamicProvisioner_CreateAmlFilesystem_SynchronousFailedReplyDefersDeletionUntilRetry(t *testing.T) {
	failed := readyTestAmlFilesystem(expectedAmlFilesystemName)
	failed.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeFailed)
	steps := []amlfsARMExchange{
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
		{method: http.MethodPut, path: amlfsTestResourceID(), status: http.StatusCreated, body: amlfsTestJSON(t, failed)},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, failed)},
		{method: http.MethodDelete, path: amlfsTestResourceID(), status: http.StatusNoContent},
	}
	scenario := newAmlfsARMScenario(t, steps)

	result, err := scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

	assert.Nil(t, result)
	require.Error(t, err)
	assert.Len(t, scenario.calls, 2)

	result, err = scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

	assert.Nil(t, result)
	assert.Equal(t, codes.Aborted, status.Code(err))
	require.ErrorContains(t, err, "deletion completed")
}

func TestDynamicProvisioner_CreateAmlFilesystem_RetryRequiresMatchingFailedObservation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*armstoragecache.AmlFilesystem)
		code   codes.Code
	}{
		{name: "ready", change: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeSucceeded)
		}, code: codes.OK},
		{name: "creating", change: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeCreating)
		}, code: codes.Aborted},
		{name: "updating", change: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeUpdating)
		}, code: codes.Aborted},
		{name: "deleting", change: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeDeleting)
		}, code: codes.Aborted},
		{name: "canceled", change: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeCanceled)
		}, code: codes.Unavailable},
		{name: "unknown state", change: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateType("Unknown"))
		}, code: codes.Unavailable},
		{name: "missing properties", change: func(f *armstoragecache.AmlFilesystem) { f.Properties = nil }, code: codes.Unavailable},
		{name: "missing identity", change: func(f *armstoragecache.AmlFilesystem) { f.ID = nil }, code: codes.Unavailable},
		{name: "wrong identity", change: func(f *armstoragecache.AmlFilesystem) { f.ID = new(amlfsTestResourceID() + "-other") }, code: codes.Unavailable},
		{name: "wrong network", change: func(f *armstoragecache.AmlFilesystem) { f.Properties.FilesystemSubnet = new("other-subnet") }, code: codes.AlreadyExists},
		{name: "missing location", change: func(f *armstoragecache.AmlFilesystem) { f.Location = nil }, code: codes.Unavailable},
		{name: "wrong initial capacity", change: func(f *armstoragecache.AmlFilesystem) { f.Properties.StorageCapacityTiB = new(float32(96)) }, code: codes.Unavailable},
		{name: "missing initial capacity", change: func(f *armstoragecache.AmlFilesystem) { f.Properties.StorageCapacityTiB = nil }, code: codes.Unavailable},
		{name: "zero initial capacity", change: func(f *armstoragecache.AmlFilesystem) { f.Properties.StorageCapacityTiB = new(float32(0)) }, code: codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			creating := readyTestAmlFilesystem(expectedAmlFilesystemName)
			creating.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeCreating)
			filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
			filesystem.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeFailed)
			test.change(&filesystem)
			steps := []amlfsARMExchange{
				{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
				{
					method: http.MethodPut, path: amlfsTestResourceID(), status: http.StatusCreated, body: amlfsTestJSON(t, creating),
					headers: http.Header{"Azure-Asyncoperation": []string{"https://management.azure.com" + amlfsTestCreateOperation}},
				},
				{method: http.MethodGet, path: amlfsTestCreateOperation, status: http.StatusOK, body: `{"status":"Failed","error":{"code":"AllocationRejected"}}`},
				{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, filesystem)},
			}
			scenario := newAmlfsARMScenario(t, steps)

			result, err := scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

			assert.Nil(t, result)
			require.Error(t, err)

			result, err = scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

			assert.Equal(t, test.code, status.Code(err), "the new observation after restart must return %s without deletion; got %v", test.code, err)
			if test.code == codes.OK {
				require.NotNil(t, result)
				assert.Equal(t, expectedMgsAddress, result.MGSAddress)
			} else {
				assert.Nil(t, result)
			}
			assert.NotContains(t, scenario.calls, http.MethodDelete+" "+amlfsTestResourceID())
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_RetryObservationErrors(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		err        error
		code       codes.Code
	}{
		{name: "verified absence", status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`, code: codes.OK},
		{name: "wrong 404", status: http.StatusNotFound, body: `{"error":{"code":"ResourceGroupNotFound"}}`, code: codes.Unavailable},
		{name: "forbidden", status: http.StatusForbidden, body: `{"error":{"code":"AuthorizationFailed"}}`, code: codes.Unavailable},
		{name: "transport", err: errors.New("connection reset"), code: codes.Unavailable},
		{name: "canceled", err: context.Canceled, code: codes.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, code: codes.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			creating := readyTestAmlFilesystem(expectedAmlFilesystemName)
			creating.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeCreating)
			steps := []amlfsARMExchange{
				{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
				{
					method: http.MethodPut, path: amlfsTestResourceID(), status: http.StatusCreated, body: amlfsTestJSON(t, creating),
					headers: http.Header{"Azure-Asyncoperation": []string{"https://management.azure.com" + amlfsTestCreateOperation}},
				},
				{method: http.MethodGet, path: amlfsTestCreateOperation, status: http.StatusOK, body: `{"status":"Failed","error":{"code":"AllocationRejected"}}`},
				{method: http.MethodGet, path: amlfsTestResourceID(), status: test.status, body: test.body, err: test.err},
			}
			if test.code == codes.OK {
				steps = append(steps, amlfsARMExchange{
					method: http.MethodPut, path: amlfsTestResourceID(), status: http.StatusOK,
					body: amlfsTestJSON(t, readyTestAmlFilesystem(expectedAmlFilesystemName)),
				})
			}
			scenario := newAmlfsARMScenario(t, steps)

			result, err := scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

			assert.Nil(t, result)
			require.Error(t, err)

			result, err = scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

			assert.Equal(t, test.code, status.Code(err), "the retry's lookup must determine the outcome, not the old operation error; got %v", err)
			if test.code == codes.OK {
				require.NotNil(t, result)
			} else {
				assert.Nil(t, result)
			}
			assert.NotContains(t, scenario.calls, http.MethodDelete+" "+amlfsTestResourceID())
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_FailedDeletionNeverRecreates(t *testing.T) {
	creating := readyTestAmlFilesystem(expectedAmlFilesystemName)
	creating.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeCreating)
	failed := readyTestAmlFilesystem(expectedAmlFilesystemName)
	failed.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeFailed)
	steps := []amlfsARMExchange{
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
		{
			method: http.MethodPut, path: amlfsTestResourceID(), status: http.StatusCreated, body: amlfsTestJSON(t, creating),
			headers: http.Header{"Azure-Asyncoperation": []string{"https://management.azure.com" + amlfsTestCreateOperation}},
		},
		{method: http.MethodGet, path: amlfsTestCreateOperation, status: http.StatusOK, body: `{"status":"Failed","error":{"code":"AllocationRejected"}}`},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, failed)},
		{
			method: http.MethodDelete, path: amlfsTestResourceID(), status: http.StatusAccepted,
			headers: http.Header{"Azure-Asyncoperation": []string{"https://management.azure.com" + amlfsTestDeleteOperation}},
		},
		{method: http.MethodGet, path: amlfsTestDeleteOperation, status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, failed)},
		{method: http.MethodDelete, path: amlfsTestResourceID(), status: http.StatusServiceUnavailable, body: `{"error":{"code":"ServiceUnavailable"}}`},
	}
	scenario := newAmlfsARMScenario(t, steps)

	result, err := scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

	assert.Nil(t, result)
	require.Error(t, err)
	assert.Len(t, scenario.calls, 3)

	result, err = scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

	assert.Nil(t, result)
	assert.Equal(t, codes.Unavailable, status.Code(err), "a missing delete operation leaves recovery unfinished")
	require.ErrorContains(t, err, "ResourceNotFound")
	assert.Len(t, scenario.calls, 6)

	result, err = scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

	assert.Nil(t, result)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Equal(t, 1, countAMLFSCall(scenario.calls, http.MethodPut+" "+amlfsTestResourceID()))
	assert.Equal(t, 2, countAMLFSCall(scenario.calls, http.MethodDelete+" "+amlfsTestResourceID()))
}

func TestDynamicProvisioner_CreateAmlFilesystem_ResourcePollingFailureDefersDeletionUntilRetry(t *testing.T) {
	creating := readyTestAmlFilesystem(expectedAmlFilesystemName)
	creating.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeCreating)
	failed := readyTestAmlFilesystem(expectedAmlFilesystemName)
	failed.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeFailed)
	steps := []amlfsARMExchange{
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
		// Without an operation URL, the SDK polls the resource itself.
		{method: http.MethodPut, path: amlfsTestResourceID(), status: http.StatusCreated, body: amlfsTestJSON(t, creating), headers: http.Header{}},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, failed)},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, failed)},
		{method: http.MethodDelete, path: amlfsTestResourceID(), status: http.StatusNoContent},
	}
	scenario := newAmlfsARMScenario(t, steps)

	result, err := scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

	assert.Nil(t, result)
	require.Error(t, err)
	assert.NotContains(t, scenario.calls, http.MethodDelete+" "+amlfsTestResourceID())

	result, err = scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

	assert.Nil(t, result)
	assert.Equal(t, codes.Aborted, status.Code(err))
	assert.Equal(t, 1, countAMLFSCall(scenario.calls, http.MethodDelete+" "+amlfsTestResourceID()))
}

func TestDynamicProvisioner_CreateAmlFilesystem_LostFinalResponseUsesFailedObservationForDeletion(t *testing.T) {
	creating := readyTestAmlFilesystem(expectedAmlFilesystemName)
	creating.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeCreating)
	failed := readyTestAmlFilesystem(expectedAmlFilesystemName)
	failed.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeFailed)
	steps := []amlfsARMExchange{
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
		{
			method: http.MethodPut, path: amlfsTestResourceID(), status: http.StatusCreated, body: amlfsTestJSON(t, creating),
			headers: http.Header{"Azure-Asyncoperation": []string{"https://management.azure.com" + amlfsTestCreateOperation}},
		},
		{method: http.MethodGet, path: amlfsTestCreateOperation, status: http.StatusOK, body: `{"status":"Succeeded"}`},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusServiceUnavailable, body: `{"error":{"code":"CreateTimeout"}}`},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, failed)},
		{method: http.MethodDelete, path: amlfsTestResourceID(), status: http.StatusNoContent},
	}
	scenario := newAmlfsARMScenario(t, steps)
	properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)

	result, err := scenario.provisioner(t).CreateAmlFilesystem(t.Context(), properties)

	assert.Nil(t, result)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.NotContains(t, scenario.calls, http.MethodDelete+" "+amlfsTestResourceID(), "a lost final response is not deletion authority")

	result, err = scenario.provisioner(t).CreateAmlFilesystem(t.Context(), properties)

	assert.Nil(t, result)
	assert.Equal(t, codes.Aborted, status.Code(err))
	require.ErrorContains(t, err, "deletion completed")
}

func TestDynamicProvisioner_CreateAmlFilesystem_LostResponseAndRestart(t *testing.T) {
	for _, test := range []struct {
		name  string
		state armstoragecache.AmlFilesystemProvisioningStateType
		code  codes.Code
	}{
		{name: "completed creation is reused", state: armstoragecache.AmlFilesystemProvisioningStateTypeSucceeded, code: codes.OK},
		{name: "creation still pending", state: armstoragecache.AmlFilesystemProvisioningStateTypeCreating, code: codes.Aborted},
		{name: "failed creation", state: armstoragecache.AmlFilesystemProvisioningStateTypeFailed, code: codes.Aborted},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
			filesystem.Properties.ProvisioningState = new(test.state)
			filesystem.Tags = map[string]*string{createdByTag: new(azureLustreDriverTag), pvNameTag: new(expectedAmlFilesystemName)}
			steps := []amlfsARMExchange{
				{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
				{method: http.MethodPut, path: amlfsTestResourceID(), err: io.ErrUnexpectedEOF},
				{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, filesystem)},
			}
			if test.state == armstoragecache.AmlFilesystemProvisioningStateTypeFailed {
				steps = append(steps, amlfsARMExchange{method: http.MethodDelete, path: amlfsTestResourceID(), status: http.StatusNoContent})
			}
			scenario := newAmlfsARMScenario(t, steps)
			properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
			result, err := scenario.provisioner(t).CreateAmlFilesystem(t.Context(), properties)
			assert.Nil(t, result)
			require.Error(t, err)

			result, err = scenario.provisioner(t).CreateAmlFilesystem(t.Context(), properties)

			assert.Equal(t, test.code, status.Code(err), "after the lost PUT reply, a new provisioner must act on %s; got %v", test.state, err)
			if test.code == codes.OK {
				require.NotNil(t, result)
				assert.Equal(t, expectedMgsAddress, result.MGSAddress)
			} else {
				assert.Nil(t, result)
			}
			assert.Equal(t, 1, countAMLFSCall(scenario.calls, http.MethodPut+" "+amlfsTestResourceID()))
			if test.state == armstoragecache.AmlFilesystemProvisioningStateTypeFailed {
				assert.Equal(t, 1, countAMLFSCall(scenario.calls, http.MethodDelete+" "+amlfsTestResourceID()))
			} else {
				assert.NotContains(t, scenario.calls, http.MethodDelete+" "+amlfsTestResourceID())
			}
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_DisablesBlindPUTRetries(t *testing.T) {
	scenario := newAmlfsARMScenario(t, []amlfsARMExchange{
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusServiceUnavailable, body: `{"error":{"code":"TransientReadFailure"}}`},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
		{method: http.MethodPut, path: amlfsTestResourceID(), err: io.ErrUnexpectedEOF},
		{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusOK, body: amlfsTestJSON(t, readyTestAmlFilesystem(expectedAmlFilesystemName))},
	})
	client, err := armstoragecache.NewAmlFilesystemsClient(expectedSubscriptionID, &azfake.TokenCredential{}, &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: scenario,
			Retry:     policy.RetryOptions{MaxRetries: 3, RetryDelay: -1},
		},
	})
	require.NoError(t, err)
	provisioner := scenario.provisioner(t)
	provisioner.amlFilesystemsClient = client
	properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)

	result, err := provisioner.CreateAmlFilesystem(t.Context(), properties)

	assert.Nil(t, result)
	require.Error(t, err)
	assert.Len(t, scenario.calls, 3, "GET retries remain enabled, but PUT retries must not bypass the existence guard")

	result, err = provisioner.CreateAmlFilesystem(t.Context(), properties)

	require.NoError(t, err)
	assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: expectedClusterSize * util.TiB}, result)
}

func TestDynamicProvisioner_CreateAmlFilesystem_PollErrorsNeverAuthorizeDeletion(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		err        error
		code       codes.Code
	}{
		{name: "poll transport reports cancellation", err: context.Canceled, code: codes.Canceled},
		{name: "poll transport reports deadline exceeded", err: context.DeadlineExceeded, code: codes.DeadlineExceeded},
		{name: "timeout message", err: errors.New("CreateTimeout: delete and try again"), code: codes.Unavailable},
		{name: "HTTP error with Failed text", status: http.StatusInternalServerError, body: `{"status":"Failed","error":{"code":"CreateTimeout"}}`, code: codes.Unavailable},
		{name: "missing operation", status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`, code: codes.Unavailable},
		{name: "canceled operation", status: http.StatusOK, body: `{"status":"Canceled","error":{"code":"Canceled"}}`, code: codes.Unavailable},
		{name: "malformed operation response", status: http.StatusOK, body: `{"status":`, code: codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			creating := readyTestAmlFilesystem(expectedAmlFilesystemName)
			creating.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeCreating)
			steps := []amlfsARMExchange{
				{method: http.MethodGet, path: amlfsTestResourceID(), status: http.StatusNotFound, body: `{"error":{"code":"ResourceNotFound"}}`},
				{
					method: http.MethodPut, path: amlfsTestResourceID(), status: http.StatusCreated, body: amlfsTestJSON(t, creating),
					headers: http.Header{"Azure-Asyncoperation": []string{"https://management.azure.com" + amlfsTestCreateOperation}},
				},
				{method: http.MethodGet, path: amlfsTestCreateOperation, status: test.status, body: test.body, err: test.err},
			}
			scenario := newAmlfsARMScenario(t, steps)

			result, err := scenario.provisioner(t).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

			assert.Nil(t, result)
			assert.Equal(t, test.code, status.Code(err), "a failed poll must return %s without authorizing deletion; got %v", test.code, err)
			assert.NotContains(t, scenario.calls, http.MethodDelete+" "+amlfsTestResourceID())
		})
	}
}

func countAMLFSCall(calls []string, expected string) int {
	count := 0
	for _, call := range calls {
		if call == expected {
			count++
		}
	}
	return count
}

func TestDynamicProvisioner_CreateAmlFilesystem_FailedRecoveryRemainsRetryable(t *testing.T) {
	for _, tc := range []struct {
		name         string
		statusCode   int
		errorCode    string
		expectedCode codes.Code
	}{
		{name: "delete permission lost", statusCode: http.StatusForbidden, errorCode: "AuthorizationFailed", expectedCode: codes.Unavailable},
		{name: "delete authentication expired", statusCode: http.StatusUnauthorized, errorCode: "AuthenticationFailed", expectedCode: codes.Unavailable},
		{name: "another operation conflicts with deletion", statusCode: http.StatusConflict, errorCode: "OperationInProgress", expectedCode: codes.Unavailable},
		{name: "delete request timed out", statusCode: http.StatusRequestTimeout, errorCode: "RequestTimeout", expectedCode: codes.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failed := readyTestAmlFilesystem(expectedAmlFilesystemName)
			failed.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeFailed)
			recorder := newMockAmlfsRecorder(nil)
			recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName] = failed
			recorder.deleteError = &azcore.ResponseError{StatusCode: tc.statusCode, ErrorCode: tc.errorCode}
			provisioner := newTestDynamicProvisioner(t, recorder)
			properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)

			result, err := provisioner.CreateAmlFilesystem(t.Context(), properties)

			require.ErrorContains(t, err, tc.errorCode)
			require.ErrorContains(t, err, "Failed provisioning recovery did not finish")
			require.ErrorContains(t, err, amlFilesystemResourceID(expectedSubscriptionID, expectedResourceGroupName, expectedAmlFilesystemName))
			assert.Equal(t, tc.expectedCode, status.Code(err))
			assert.Nil(t, result)
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get", "AmlFilesystemsServerTransport.BeginDelete"}, recorder.fakeCallCount)
			assert.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName)

			recorder.deleteError = nil
			result, err = provisioner.CreateAmlFilesystem(t.Context(), properties)

			assert.Nil(t, result)
			require.ErrorContains(t, err, "deletion completed, retry creation")
			assert.Equal(t, codes.Aborted, status.Code(err))
			assert.Empty(t, recorder.recordedAmlfsConfigurations)
			assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginCreateOrUpdate",
				"recreation must wait for a new invocation that observes verified absence")

			result, err = provisioner.CreateAmlFilesystem(t.Context(), properties)

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, expectedMgsAddress, result.MGSAddress)
		})
	}
}

func TestDynamicProvisioner_DeleteAmlFilesystem_MissingOperationDoesNotProveBackendDeletion(t *testing.T) {
	recorder := newMockAmlfsRecorder(nil)
	recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName] = armstoragecache.AmlFilesystem{
		Name: new(expectedAmlFilesystemName),
	}
	recorder.deletePollingError = &azcore.ResponseError{StatusCode: http.StatusNotFound, ErrorCode: "ResourceNotFound"}
	provisioner := newTestDynamicProvisioner(t, recorder)

	err := provisioner.DeleteAmlFilesystem(t.Context(), expectedResourceGroupName, expectedAmlFilesystemName)

	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName, "a failed status poll must not establish successful deletion")
	assert.Equal(t, []string{"AmlFilesystemsServerTransport.BeginDelete"}, recorder.fakeCallCount)
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Err_NilCLient(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	dynamicProvisioner.amlFilesystemsClient = nil

	err := dynamicProvisioner.DeleteAmlFilesystem(t.Context(), expectedResourceGroupName, expectedAmlFilesystemName)
	require.ErrorContains(t, err, "aml filesystem client is nil")
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Err_Timeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := newMockAmlfsRecorder([]string{})
		dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

		_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))
		require.NoError(t, err)

		// Use 1.5x the poll frequency so the deadline expires between poll ticks
		// rather than coinciding with one. Aligning with a tick creates a select
		// race between time.After and ctx.Done in the SDK's Delay helper.
		ctx, cancel := context.WithTimeout(t.Context(), 3*quickPollFrequency/2)
		defer cancel()
		err = dynamicProvisioner.DeleteAmlFilesystem(ctx, expectedResourceGroupName, expectedAmlFilesystemName)
		require.Error(t, err)
		grpcStatus, ok := status.FromError(err)
		require.True(t, ok, "error should be a gRPC status, got: %v", err)
		assert.Equal(t, codes.DeadlineExceeded, grpcStatus.Code())
		require.ErrorContains(t, err, "context deadline exceeded")
	})
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Err_ImmediateFailure(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	amlFilesystemName := immediateDeleteFailureName

	_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(amlFilesystemName))
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)

	err = dynamicProvisioner.DeleteAmlFilesystem(t.Context(), expectedResourceGroupName, amlFilesystemName)
	require.ErrorContains(t, err, immediateDeleteFailureName)
	assert.Len(t, recorder.recordedAmlfsConfigurations, 1)
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Err_EventualFailure(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)

	amlFilesystemName := eventualDeleteFailureName

	_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(amlFilesystemName))
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)

	err = dynamicProvisioner.DeleteAmlFilesystem(t.Context(), expectedResourceGroupName, amlFilesystemName)
	require.ErrorContains(t, err, eventualDeleteFailureName)
	assert.Len(t, recorder.recordedAmlfsConfigurations, 1)
}

func TestDynamicProvisioner_DeleteAmlFilesystem_Success_DeletesCorrectCluster(t *testing.T) {
	otherAmlFilesystemName := expectedAmlFilesystemName + "2"

	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	require.Empty(t, recorder.recordedAmlfsConfigurations)
	_, err := dynamicProvisioner.CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))
	require.NoError(t, err)
	_, err = dynamicProvisioner.CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(otherAmlFilesystemName))
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 2)

	err = dynamicProvisioner.DeleteAmlFilesystem(t.Context(), expectedResourceGroupName, expectedAmlFilesystemName)
	require.NoError(t, err)
	require.Len(t, recorder.recordedAmlfsConfigurations, 1)
	assert.Equal(t, otherAmlFilesystemName, *recorder.recordedAmlfsConfigurations[otherAmlFilesystemName].Name)
}

func TestDynamicProvisioner_GetAmlFilesystem_AcceptsTypedResourceNotFound(t *testing.T) {
	testCases := []struct {
		name     string
		getError error
	}{
		{name: "empty backend returns HTTP 404 ResourceNotFound"},
		{name: "wrapped typed response", getError: fmt.Errorf("looking up AMLFS: %w", &azcore.ResponseError{
			StatusCode: http.StatusNotFound, ErrorCode: "ResourceNotFound",
		})},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			recorder.getError = test.getError
			provisioner := newTestDynamicProvisioner(t, recorder)

			result, err := provisioner.GetAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

			require.NoError(t, err)
			assert.Nil(t, result)
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)
		})
	}
}

func TestDynamicProvisioner_CreateAmlFilesystem_DoesNotMutateWhenLookupFails(t *testing.T) {
	testCases := []struct {
		name         string
		getError     error
		expectedCode codes.Code
	}{
		{name: "untyped message", getError: errors.New("ResourceNotFound"), expectedCode: codes.Unavailable},
		{name: "synthetic gRPC status lacks ARM absence evidence", getError: status.Error(codes.NotFound, "ResourceNotFound"), expectedCode: codes.Unavailable},
		{name: "missing resource group", getError: &azcore.ResponseError{
			StatusCode: http.StatusNotFound, ErrorCode: "ResourceGroupNotFound",
		}, expectedCode: codes.Unavailable},
		{name: "missing operation", getError: &azcore.ResponseError{
			StatusCode: http.StatusNotFound, ErrorCode: "OperationNotFound",
		}, expectedCode: codes.Unavailable},
		{name: "missing error code", getError: &azcore.ResponseError{
			StatusCode: http.StatusNotFound,
		}, expectedCode: codes.Unavailable},
		{name: "missing status code", getError: &azcore.ResponseError{
			ErrorCode: "ResourceNotFound",
		}, expectedCode: codes.Unavailable},
		{name: "permission failure mentioning absence", getError: fmt.Errorf("ResourceNotFound text: %w", &azcore.ResponseError{
			StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed",
		}), expectedCode: codes.Unavailable},
		{name: "wrong status with absence code", getError: &azcore.ResponseError{
			StatusCode: http.StatusServiceUnavailable, ErrorCode: "ResourceNotFound",
		}, expectedCode: codes.Unavailable},
		{name: "unauthenticated", getError: &azcore.ResponseError{
			StatusCode: http.StatusUnauthorized, ErrorCode: "AuthenticationFailed",
		}, expectedCode: codes.Unavailable},
		{name: "throttled", getError: &azcore.ResponseError{
			StatusCode: http.StatusTooManyRequests, ErrorCode: "TooManyRequests",
		}, expectedCode: codes.Unavailable},
		{name: "canceled", getError: context.Canceled, expectedCode: codes.Canceled},
		{name: "deadline exceeded", getError: context.DeadlineExceeded, expectedCode: codes.DeadlineExceeded},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			recorder.getError = test.getError
			provisioner := newTestDynamicProvisioner(t, recorder)

			mgsAddress, err := provisioner.CreateAmlFilesystem(t.Context(), &AmlFilesystemProperties{
				ResourceGroupName: expectedResourceGroupName,
				AmlFilesystemName: expectedAmlFilesystemName,
				SubnetInfo:        buildExpectedSubnetInfo(),
			})

			require.Error(t, err)
			assert.Equal(t, test.expectedCode, status.Code(err),
				"lookup failure must report %s before any mutation; got %v", test.expectedCode, err)
			assert.Empty(t, mgsAddress)
			assert.Empty(t, recorder.recordedAmlfsConfigurations)
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount, "failed observation must not create or delete storage")
		})
	}
}

func TestDynamicProvisioner_CheckSubnetCapacity_Success(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	hasSufficientCapacity, err := dynamicProvisioner.CheckSubnetCapacity(t.Context(), buildExpectedSubnetInfo(), expectedSku, expectedClusterSize)
	require.NoError(t, err)
	assert.True(t, hasSufficientCapacity)
}

func TestDynamicProvisioner_CheckSubnetCapacity_FullVnet(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	subnetInfo := buildExpectedSubnetInfo()
	subnetInfo.VnetName = fullVnetName
	hasSufficientCapacity, err := dynamicProvisioner.CheckSubnetCapacity(t.Context(), subnetInfo, expectedSku, expectedClusterSize)
	require.NoError(t, err)
	assert.False(t, hasSufficientCapacity)
}

// TestDynamicProvisioner_CheckSubnetCapacity_Success_CaseInsensitiveSubnetIDMatch
// covers GitHub issue #290: the subnet ID the driver builds locally must
// still match the subnet ID the ARM VirtualNetworks ListUsage API returns
// even if that API echoes back different casing, since
// Azure resource group names are case-insensitive.
func TestDynamicProvisioner_CheckSubnetCapacity_Success_CaseInsensitiveSubnetIDMatch(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	subnetInfo := buildExpectedSubnetInfo()
	subnetInfo.VnetName = caseMismatchVnetName
	// subnetInfo.SubnetID keeps expectedAmlFilesystemSubnetID's normal casing,
	// while the fake ARM server (keyed off caseMismatchVnetName) returns the
	// same subnet ID as caseMismatchAmlFilesystemSubnetID (upper-cased).
	hasSufficientCapacity, err := dynamicProvisioner.CheckSubnetCapacity(t.Context(), subnetInfo, expectedSku, expectedClusterSize)
	require.NoError(t, err)
	assert.True(t, hasSufficientCapacity)
}

func TestDynamicProvisioner_CheckSubnetCapacity_Err_NilMgmtClient(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	dynamicProvisioner.mgmtClient = nil

	_, err := dynamicProvisioner.CheckSubnetCapacity(t.Context(), buildExpectedSubnetInfo(), expectedSku, expectedClusterSize)
	assert.ErrorContains(t, err, "storage management client is nil")
}

func TestDynamicProvisioner_CheckSubnetCapacity_Err_NilVnetClient(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	dynamicProvisioner.vnetClient = nil

	_, err := dynamicProvisioner.CheckSubnetCapacity(t.Context(), buildExpectedSubnetInfo(), expectedSku, expectedClusterSize)
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
			recorder := newMockAmlfsRecorder([]string{})
			dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
			require.Empty(t, recorder.recordedAmlfsConfigurations)

			_, err := dynamicProvisioner.CheckSubnetCapacity(t.Context(), tC.subnetProperties, tC.sku, expectedClusterSize)
			assert.ErrorContains(t, err, tC.expectedError)
		})
	}
}

func TestDynamicProvisioner_GetSkuValuesForLocation_Success(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	expectedZones := []string{"zone1", "zone2", "zone3"}
	expectedSkuValues := map[string]*LustreSkuValue{
		expectedSku:         {IncrementInTib: 4, MaximumInTib: 128, AvailableZones: expectedZones},
		otherSkuForLocation: {IncrementInTib: 4, MaximumInTib: 128, AvailableZones: expectedZones},
	}

	skuValues, err := dynamicProvisioner.GetSkuValuesForLocation(t.Context(), expectedLocation)
	t.Logf("SKU values: %#v", skuValues)
	require.NoError(t, err)
	require.Len(t, skuValues, 2)
	assert.Equal(t, expectedSkuValues, skuValues)
}

func TestDynamicProvisioner_GetSkuValuesForLocation_Err_NilClient(t *testing.T) {
	recorder := newMockAmlfsRecorder([]string{})
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)
	dynamicProvisioner.skusClient = nil

	skuValues, err := dynamicProvisioner.GetSkuValuesForLocation(t.Context(), expectedLocation)
	t.Log(err)
	require.Nil(t, skuValues)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, grpcStatus.Code())
	require.ErrorContains(t, err, "skus client is nil")
	assert.Empty(t, recorder.recordedAmlfsConfigurations)
}

func TestDynamicProvisioner_GetSkuValuesForLocation_AllowsEmptyAdvertisedZoneList(t *testing.T) {
	recorder := newMockAmlfsRecorder(nil)
	recorder.skuResponse = &armstoragecache.ResourceSKUsResult{Value: []*armstoragecache.ResourceSKU{
		newResourceSku(AmlfsSkuResourceType, expectedSku, expectedLocation, expectedSkuIncrement, expectedSkuMaximum, []string{}),
	}}
	dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

	skuValues, err := dynamicProvisioner.GetSkuValuesForLocation(t.Context(), expectedLocation)
	require.NoError(t, err)
	require.Len(t, skuValues, 1)

	// Verify the SKU exists but has no zones
	skuValue, exists := skuValues[expectedSku]
	require.True(t, exists, "expected SKU should exist")
	assert.Equal(t, int64(4), skuValue.IncrementInTib)
	assert.Equal(t, int64(128), skuValue.MaximumInTib)
	assert.Empty(t, skuValue.AvailableZones, "expected no zones to be available")
}

func TestDynamicProvisioner_GetSkuValuesForLocation_Errors(t *testing.T) {
	testCases := []struct {
		desc             string
		failureBehaviors []string
		expectedCode     codes.Code
		expectedError    string
	}{
		{
			desc:             "No AMLFS SKUs",
			failureBehaviors: []string{noAmlfsSkus},
			expectedCode:     codes.Internal,
			expectedError:    "found no AMLFS SKUs",
		},
		{
			desc:             "No AMLFS SKUs for location",
			failureBehaviors: []string{noAmlfsSkusForLocation},
			expectedCode:     codes.Internal,
			expectedError:    "found no AMLFS SKUs",
		},
		{
			desc:             "Invalid SKU increment",
			failureBehaviors: []string{invalidSkuIncrement},
			expectedCode:     codes.Unavailable,
			expectedError:    "invalid SKU response",
		},
		{
			desc:             "Invalid SKU maximum",
			failureBehaviors: []string{invalidSkuMaximum},
			expectedCode:     codes.Unavailable,
			expectedError:    "invalid SKU response",
		},
		{
			desc:             "No location info for SKU",
			failureBehaviors: []string{noLocationInfoForSku},
			expectedCode:     codes.Unavailable,
			expectedError:    "missing location info",
		},
		{
			desc:             "Invalid location",
			failureBehaviors: []string{errorLocation},
			expectedCode:     codes.Unknown,
			expectedError:    "error retrieving SKUs",
		},
		{
			desc:             "Forbidden from ARM",
			failureBehaviors: []string{forbiddenLocation},
			expectedCode:     codes.PermissionDenied,
			expectedError:    "error retrieving SKUs",
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(tC.failureBehaviors)
			dynamicProvisioner := newTestDynamicProvisioner(t, recorder)

			skuValues, err := dynamicProvisioner.GetSkuValuesForLocation(t.Context(), expectedLocation)
			require.Nil(t, skuValues)
			t.Log(err)
			require.Error(t, err)
			grpcStatus, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tC.expectedCode, grpcStatus.Code())
			require.ErrorContains(t, err, tC.expectedError)
			assert.Empty(t, recorder.recordedAmlfsConfigurations)
		})
	}
}

func TestConvertStatusCodeErrorToGrpcCodeError(t *testing.T) {
	tests := []struct {
		name                string
		inputError          error
		expectedCode        codes.Code
		expectedMsgContains string
	}{
		{
			name:         "BadRequest",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusBadRequest},
			expectedCode: codes.InvalidArgument,
		},
		{
			name:         "RequestTimeout",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusRequestTimeout},
			expectedCode: codes.DeadlineExceeded,
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
			name:                "Forbidden",
			inputError:          &azcore.ResponseError{StatusCode: http.StatusForbidden},
			expectedCode:        codes.PermissionDenied,
			expectedMsgContains: "required RBAC role assignments",
		},
		{
			name:                "Unauthorized",
			inputError:          &azcore.ResponseError{StatusCode: http.StatusUnauthorized},
			expectedCode:        codes.Unauthenticated,
			expectedMsgContains: "required RBAC role assignments",
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
			name:                "Canceled",
			inputError:          context.Canceled,
			expectedCode:        codes.Canceled,
			expectedMsgContains: "context canceled",
		},
		{
			name:                "DeadlineExceeded",
			inputError:          context.DeadlineExceeded,
			expectedCode:        codes.DeadlineExceeded,
			expectedMsgContains: "context deadline exceeded",
		},
		{
			name:         "InternalExecutionError",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusOK, ErrorCode: "InternalExecutionError"},
			expectedCode: codes.DeadlineExceeded,
		},
		{
			name:         "AscInternalError",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusOK, ErrorCode: "AscInternalError"},
			expectedCode: codes.DeadlineExceeded,
		},
		{
			name:         "DeploymentFailed",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusOK, ErrorCode: "DeploymentFailed"},
			expectedCode: codes.DeadlineExceeded,
		},
		{
			name:         "DeploymentFailure",
			inputError:   &azcore.ResponseError{StatusCode: http.StatusOK, ErrorCode: "DeploymentFailure"},
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
			if tt.inputError == nil {
				err := convertHTTPResponseErrorToGrpcCodeError(nil)
				require.NoError(t, err)
				return
			}
			inputs := []struct {
				name string
				err  error
			}{
				{name: "Direct", err: tt.inputError},
				{name: "Wrapped", err: fmt.Errorf("wrapped error: %w", tt.inputError)},
			}
			for _, input := range inputs {
				t.Run(input.name, func(t *testing.T) {
					err := convertHTTPResponseErrorToGrpcCodeError(input.err)
					grpcStatus, ok := status.FromError(err)
					require.True(t, ok, "converted error must be a gRPC status")
					assert.Equal(t, tt.expectedCode, grpcStatus.Code())
					if tt.expectedMsgContains != "" {
						assert.Contains(t, grpcStatus.Message(), tt.expectedMsgContains)
					}
				})
			}
		})
	}
}
