/*
Copyright 2026 The Kubernetes Authors.

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
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/util"
)

func TestCreateVolume_SKUDiscoveryUnavailable(t *testing.T) {
	tests := []struct {
		name          string
		skuError      error
		sku           string
		zone          string
		requiredBytes int64
		expectedBytes int64
	}{
		{
			name:          "permission denial preserves the configured SKU and zone",
			skuError:      status.Error(codes.PermissionDenied, "subscription SKU listing is not authorized"),
			sku:           "AMLFS-Durable-Premium-250",
			zone:          "1",
			requiredBytes: 16 * util.TiB,
			expectedBytes: 16 * util.TiB,
		},
		{
			name:          "unavailable discovery does not invent an advertised SKU or zone list",
			skuError:      status.Error(codes.Unavailable, "SKU service is unavailable"),
			sku:           "future-sku",
			zone:          "4",
			requiredBytes: 48 * util.TiB,
			expectedBytes: 48 * util.TiB,
		},
		{
			name:          "unknown zone support allows an omitted zone and retains default capacity",
			skuError:      status.Error(codes.Internal, "SKU response has no location metadata"),
			sku:           "AMLFS-Durable-Premium-500",
			zone:          "",
			requiredBytes: 0,
			expectedBytes: 4 * util.TiB,
		},
		{
			name:          "unknown capacity increment rounds nine TiB to the four TiB fallback",
			skuError:      status.Error(codes.PermissionDenied, "subscription SKU listing is not authorized"),
			sku:           "AMLFS-Durable-Premium-500",
			zone:          "1",
			requiredBytes: 9 * util.TiB,
			expectedBytes: 12 * util.TiB,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			driver := NewFakeDriver(t)
			provisioner := &FakeDynamicProvisioner{skuError: tc.skuError}
			driver.dynamicProvisioner = provisioner
			request := buildDynamicProvCreateVolumeRequest()
			request.Name = "pvc-without-sku-discovery"
			request.Parameters[VolumeContextSkuName] = tc.sku
			request.Parameters[VolumeContextZone] = tc.zone
			request.CapacityRange = &csi.CapacityRange{RequiredBytes: tc.requiredBytes}

			response, err := driver.CreateVolume(t.Context(), request)

			require.NoError(t, err)
			assert.Equal(t, tc.expectedBytes, response.GetVolume().GetCapacityBytes())
			require.Len(t, provisioner.Filesystems, 1, "the creation attempt must reach the backend")
			assert.Equal(t, tc.sku, provisioner.Filesystems[0].SKUName)
			assert.Equal(t, tc.zone, provisioner.Filesystems[0].Zone)
			assert.InDelta(t, float32(tc.expectedBytes)/util.TiB, provisioner.Filesystems[0].StorageCapacityTiB, 0)
			assert.Equal(t, map[string]int{"GetSkuValuesForLocation": 1, "CreateAmlFilesystem": 1}, provisioner.fakeCallCount)
		})
	}
}

func TestCreateVolume_SKUDiscoveryFailureStillEnforcesCapacityLimit(t *testing.T) {
	driver := NewFakeDriver(t)
	provisioner := &FakeDynamicProvisioner{
		skuError: status.Error(codes.PermissionDenied, "subscription SKU listing is not authorized"),
	}
	driver.dynamicProvisioner = provisioner
	request := buildDynamicProvCreateVolumeRequest()
	request.CapacityRange = &csi.CapacityRange{
		RequiredBytes: 9 * util.TiB,
		LimitBytes:    10 * util.TiB,
	}

	response, err := driver.CreateVolume(t.Context(), request)

	require.ErrorContains(t, err, "greater than capacity limit")
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Nil(t, response)
	assert.Equal(t, map[string]int{"GetSkuValuesForLocation": 1}, provisioner.fakeCallCount,
		"rounding to twelve TiB must not bypass the caller's ten TiB limit")
}

func TestCreateVolume_SKUDiscoveryFailureHonorsCallerCancellation(t *testing.T) {
	driver := NewFakeDriver(t)
	provisioner := &FakeDynamicProvisioner{skuError: status.Error(codes.Canceled, "SKU lookup canceled")}
	driver.dynamicProvisioner = provisioner
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	response, err := driver.CreateVolume(ctx, buildDynamicProvCreateVolumeRequest())

	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))
	assert.Nil(t, response)
	assert.NotContains(t, provisioner.fakeCallCount, "CreateAmlFilesystem")
}

func TestCreateVolume_SKUDiscoveryFailureHonorsExpiredDeadline(t *testing.T) {
	driver := NewFakeDriver(t)
	provisioner := &FakeDynamicProvisioner{skuError: status.Error(codes.DeadlineExceeded, "SKU lookup timed out")}
	driver.dynamicProvisioner = provisioner
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancel()

	response, err := driver.CreateVolume(ctx, buildDynamicProvCreateVolumeRequest())

	require.Error(t, err)
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
	assert.Nil(t, response)
	assert.NotContains(t, provisioner.fakeCallCount, "CreateAmlFilesystem")
}

func TestCreateVolume_SKUDiscoveryFailurePreservesCreateError(t *testing.T) {
	driver := NewFakeDriver(t)
	provisioner := &FakeDynamicProvisioner{
		skuError: status.Error(codes.Unavailable, "SKU lookup unavailable"),
	}
	driver.dynamicProvisioner = provisioner
	request := buildDynamicProvCreateVolumeRequest()
	request.Name = clusterRequestFailureName

	response, err := driver.CreateVolume(t.Context(), request)

	require.ErrorContains(t, err, clusterRequestFailureName)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Nil(t, response)
	assert.Equal(t, map[string]int{"GetSkuValuesForLocation": 1, "CreateAmlFilesystem": 1}, provisioner.fakeCallCount)
	assert.Empty(t, provisioner.Filesystems)
}

func TestCreateVolume_OptionalMetadataPermissionCombinationsReachARM(t *testing.T) {
	tests := []struct {
		name          string
		skuAllowed    bool
		sizingAllowed bool
		usageAllowed  bool
	}{
		{name: "no optional permissions"},
		{name: "only SKU lookup allowed", skuAllowed: true},
		{name: "only subnet sizing allowed", sizingAllowed: true},
		{name: "only subnet usage allowed", usageAllowed: true},
		{name: "SKU and sizing allowed but usage denied", skuAllowed: true, sizingAllowed: true},
		{name: "SKU and usage allowed but sizing denied", skuAllowed: true, usageAllowed: true},
		{name: "sizing and usage allowed but SKU denied", sizingAllowed: true, usageAllowed: true},
		{name: "all optional permissions", skuAllowed: true, sizingAllowed: true, usageAllowed: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			if !tc.skuAllowed {
				recorder.skuError = &azcore.ResponseError{StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed"}
			}
			if !tc.sizingAllowed {
				recorder.subnetSizeError = &azcore.ResponseError{StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed"}
			}
			if !tc.usageAllowed {
				recorder.subnetUsageError = &azcore.ResponseError{StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed"}
			}
			recorder.subnetUsageResponse = &armnetwork.VirtualNetworkListUsageResult{Value: []*armnetwork.VirtualNetworkUsage{{
				ID:           new("/subscriptions/fake-subscription-id/resourceGroups/fake-resource-group/providers/Microsoft.Network/virtualNetworks/fake-vnet/subnets/fake-subnet-name"),
				CurrentValue: new(float64(expectedUsedIPCount)),
				Limit:        new(float64(expectedTotalIPCount)),
			}}}
			driver := NewFakeDriver(t)
			driver.cloud.SubscriptionID = "fake-subscription-id"
			driver.dynamicProvisioner = newTestDynamicProvisioner(t, recorder)
			request := buildDynamicProvCreateVolumeRequest()
			request.Name = expectedAmlFilesystemName
			request.Parameters[VolumeContextResourceGroupName] = expectedResourceGroupName
			request.Parameters[VolumeContextLocation] = expectedLocation
			request.Parameters[VolumeContextSkuName] = expectedSku
			request.Parameters[VolumeContextVnetResourceGroup] = expectedResourceGroupName
			request.Parameters[VolumeContextVnetName] = expectedVnetName
			request.Parameters[VolumeContextSubnetName] = expectedAmlFilesystemSubnetName
			request.Parameters[VolumeContextZone] = "zone1"
			request.CapacityRange = &csi.CapacityRange{RequiredBytes: 48 * util.TiB}

			response, err := driver.CreateVolume(t.Context(), request)

			require.NoError(t, err)
			assert.Equal(t, int64(48*util.TiB), response.GetVolume().GetCapacityBytes())
			assert.Equal(t, expectedMgsAddress, response.GetVolume().GetVolumeContext()[VolumeContextMGSIPAddress])
			require.Len(t, recorder.recordedAmlfsConfigurations, 1)
			require.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName)
			created := recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName]
			require.NotNil(t, created.SKU)
			require.NotNil(t, created.SKU.Name)
			assert.Equal(t, expectedSku, *created.SKU.Name)
			require.Len(t, created.Zones, 1)
			assert.Equal(t, "zone1", *created.Zones[0])
			require.NotNil(t, created.Properties)
			require.NotNil(t, created.Properties.StorageCapacityTiB)
			assert.InDelta(t, 48, *created.Properties.StorageCapacityTiB, 0)
			assert.Contains(t, recorder.fakeCallCount, "SKUsServerTransport.NewListPager")
			assert.Contains(t, recorder.fakeCallCount, "ManagementServerTransport.GetRequiredAmlFSSubnetsSize")
			assert.Contains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginCreateOrUpdate")
			if tc.sizingAllowed {
				assert.Contains(t, recorder.fakeCallCount, "VirtualNetworksServerTransport.NewListUsagePager")
			} else {
				assert.NotContains(t, recorder.fakeCallCount, "VirtualNetworksServerTransport.NewListUsagePager")
			}
			assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginDelete")
		})
	}
}

func TestDynamicProvisioner_SubnetUsagePermissionFallbackOnRepeatedChecks(t *testing.T) {
	recorder := newMockAmlfsRecorder(nil)
	recorder.subnetUsageError = &azcore.ResponseError{StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed"}
	provisioner := newTestDynamicProvisioner(t, recorder)

	for check := range 2 {
		allowed, err := provisioner.CheckSubnetCapacity(t.Context(), buildExpectedSubnetInfo(), expectedSku, expectedClusterSize)

		require.NoError(t, err, "subnet capacity check %d", check+1)
		assert.True(t, allowed, "subnet capacity check %d", check+1)
	}
	assert.Equal(t, []string{
		"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
		"VirtualNetworksServerTransport.NewListUsagePager",
		"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
		"VirtualNetworksServerTransport.NewListUsagePager",
	}, recorder.fakeCallCount)
}

func TestDynamicProvisioner_SubnetPermissionFallbackPreservesCreateErrors(t *testing.T) {
	tests := []struct {
		name            string
		createError     error
		pollingError    *azcore.ResponseError
		expectedCode    codes.Code
		expectedMessage string
	}{
		{
			name:            "ARM rejects an unavailable SKU during submission",
			createError:     &azcore.ResponseError{StatusCode: http.StatusBadRequest, ErrorCode: "SkuNotAvailable"},
			expectedCode:    codes.InvalidArgument,
			expectedMessage: "SkuNotAvailable",
		},
		{
			name:            "ARM rejects capacity while polling the accepted creation",
			pollingError:    &azcore.ResponseError{StatusCode: http.StatusBadRequest, ErrorCode: "InvalidStorageCapacity"},
			expectedCode:    codes.InvalidArgument,
			expectedMessage: "InvalidStorageCapacity",
		},
		{
			name:            "missing resource group creation permission is still an error",
			createError:     &azcore.ResponseError{StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed"},
			expectedCode:    codes.PermissionDenied,
			expectedMessage: "required RBAC role assignments",
		},
	}
	for _, tc := range tests {
		for _, deniedCheck := range []string{"sizing", "usage"} {
			t.Run(tc.name+"/"+deniedCheck+" permission denied", func(t *testing.T) {
				recorder := newMockAmlfsRecorder(nil)
				expectedCalls := []string{
					"AmlFilesystemsServerTransport.Get",
					"ManagementServerTransport.GetRequiredAmlFSSubnetsSize",
				}
				if deniedCheck == "sizing" {
					recorder.subnetSizeError = &azcore.ResponseError{StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed"}
				} else {
					recorder.subnetUsageError = &azcore.ResponseError{StatusCode: http.StatusForbidden, ErrorCode: "AuthorizationFailed"}
					expectedCalls = append(expectedCalls, "VirtualNetworksServerTransport.NewListUsagePager")
				}
				recorder.createError = tc.createError
				recorder.createPollingError = tc.pollingError
				provisioner := newTestDynamicProvisioner(t, recorder)
				properties := &AmlFilesystemProperties{
					ResourceGroupName:    expectedResourceGroupName,
					AmlFilesystemName:    expectedAmlFilesystemName,
					Location:             expectedLocation,
					SKUName:              expectedSku,
					StorageCapacityTiB:   expectedClusterSize,
					SubnetInfo:           buildExpectedSubnetInfo(),
					MaintenanceDayOfWeek: "Saturday",
					TimeOfDayUTC:         "12:00",
				}

				result, err := provisioner.CreateAmlFilesystem(t.Context(), properties)

				require.ErrorContains(t, err, tc.expectedMessage)
				assert.Equal(t, tc.expectedCode, status.Code(err))
				assert.Empty(t, result)
				expectedCalls = append(expectedCalls, "AmlFilesystemsServerTransport.BeginCreateOrUpdate")
				assert.Equal(t, expectedCalls, recorder.fakeCallCount,
					"a service rejection must not cause a delete or an automatic second create")
			})
		}
	}
}

func TestCreateVolume_SubnetCapacityFailuresStillBlockCreation(t *testing.T) {
	tests := []struct {
		name         string
		usageError   error
		usedIPs      float64
		expectedCode codes.Code
	}{
		{
			name:         "usage service unavailable",
			usageError:   &azcore.ResponseError{StatusCode: http.StatusServiceUnavailable, ErrorCode: "ServiceUnavailable"},
			expectedCode: codes.Unavailable,
		},
		{
			name:         "usage authentication failure",
			usageError:   &azcore.ResponseError{StatusCode: http.StatusUnauthorized, ErrorCode: "AuthenticationFailed"},
			expectedCode: codes.Unauthenticated,
		},
		{
			name:         "usage request rejected",
			usageError:   &azcore.ResponseError{StatusCode: http.StatusBadRequest, ErrorCode: "InvalidRequest"},
			expectedCode: codes.InvalidArgument,
		},
		{
			name:         "known insufficient subnet capacity",
			usedIPs:      expectedTotalIPCount,
			expectedCode: codes.ResourceExhausted,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			recorder.subnetUsageError = tc.usageError
			recorder.subnetUsageResponse = &armnetwork.VirtualNetworkListUsageResult{Value: []*armnetwork.VirtualNetworkUsage{{
				ID:           new("/subscriptions/fake-subscription-id/resourceGroups/fake-resource-group/providers/Microsoft.Network/virtualNetworks/fake-vnet/subnets/fake-subnet-name"),
				CurrentValue: new(tc.usedIPs),
				Limit:        new(float64(expectedTotalIPCount)),
			}}}
			driver := NewFakeDriver(t)
			driver.cloud.SubscriptionID = "fake-subscription-id"
			driver.dynamicProvisioner = newTestDynamicProvisioner(t, recorder)
			request := buildDynamicProvCreateVolumeRequest()
			request.Name = expectedAmlFilesystemName
			request.Parameters[VolumeContextResourceGroupName] = expectedResourceGroupName
			request.Parameters[VolumeContextLocation] = expectedLocation
			request.Parameters[VolumeContextSkuName] = expectedSku
			request.Parameters[VolumeContextVnetResourceGroup] = expectedResourceGroupName
			request.Parameters[VolumeContextVnetName] = expectedVnetName
			request.Parameters[VolumeContextSubnetName] = expectedAmlFilesystemSubnetName
			request.Parameters[VolumeContextZone] = "zone1"
			request.CapacityRange = &csi.CapacityRange{RequiredBytes: 48 * util.TiB}

			response, err := driver.CreateVolume(t.Context(), request)

			require.Error(t, err)
			assert.Equal(t, tc.expectedCode, status.Code(err))
			assert.Nil(t, response)
			assert.Empty(t, recorder.recordedAmlfsConfigurations)
			assert.Contains(t, recorder.fakeCallCount, "ManagementServerTransport.GetRequiredAmlFSSubnetsSize")
			assert.Contains(t, recorder.fakeCallCount, "VirtualNetworksServerTransport.NewListUsagePager")
			assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginCreateOrUpdate")
			assert.NotContains(t, recorder.fakeCallCount, "AmlFilesystemsServerTransport.BeginDelete")
		})
	}
}

func TestDynamicProvisioner_SubnetSizingOnlySkipsPermissionDenial(t *testing.T) {
	tests := []struct {
		name         string
		apiError     *azcore.ResponseError
		expectedCode codes.Code
	}{
		{
			name:         "sizing API rejects the SKU",
			apiError:     &azcore.ResponseError{StatusCode: http.StatusBadRequest, ErrorCode: "InvalidSku"},
			expectedCode: codes.InvalidArgument,
		},
		{
			name:         "sizing service is temporarily unavailable",
			apiError:     &azcore.ResponseError{StatusCode: http.StatusServiceUnavailable, ErrorCode: "ServiceUnavailable"},
			expectedCode: codes.Unavailable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			recorder.subnetSizeError = tc.apiError
			provisioner := newTestDynamicProvisioner(t, recorder)

			allowed, err := provisioner.CheckSubnetCapacity(t.Context(), buildExpectedSubnetInfo(), expectedSku, expectedClusterSize)

			require.ErrorContains(t, err, tc.apiError.ErrorCode)
			assert.Equal(t, tc.expectedCode, status.Code(err))
			assert.False(t, allowed)
			assert.Equal(t, []string{"ManagementServerTransport.GetRequiredAmlFSSubnetsSize"}, recorder.fakeCallCount)
		})
	}
}

type subnetCapacityTransportFunc func(*http.Request) (*http.Response, error)

func (f subnetCapacityTransportFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDynamicProvisioner_SubnetSizingHonorsCancellationDuringRequest(t *testing.T) {
	recorder := newMockAmlfsRecorder(nil)
	provisioner := newTestDynamicProvisioner(t, recorder)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	requests := 0
	clientFactory, err := armstoragecache.NewClientFactory("fake-subscription-id", &azfake.TokenCredential{}, &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: subnetCapacityTransportFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				assert.Contains(t, request.URL.Path, "/getRequiredAmlFSSubnetsSize")
				cancel()
				return nil, request.Context().Err()
			}),
		},
	})
	require.NoError(t, err)
	provisioner.mgmtClient = clientFactory.NewManagementClient()

	allowed, err := provisioner.CheckSubnetCapacity(ctx, buildExpectedSubnetInfo(), expectedSku, expectedClusterSize)

	require.Error(t, err)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.Equal(t, codes.Canceled, status.Code(err))
	assert.False(t, allowed)
	assert.Equal(t, 1, requests, "the caller cancels during the sizing request, before any subnet-usage request")
	assert.NotContains(t, recorder.fakeCallCount, "VirtualNetworksServerTransport.NewListUsagePager")
}

func TestDynamicProvisioner_SubnetUsageHonorsCancellationDuringRequest(t *testing.T) {
	recorder := newMockAmlfsRecorder(nil)
	provisioner := newTestDynamicProvisioner(t, recorder)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	requests := 0
	clientFactory, err := armnetwork.NewClientFactory("fake-subscription-id", &azfake.TokenCredential{}, &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: subnetCapacityTransportFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				assert.Contains(t, request.URL.Path, "/usages")
				cancel()
				return nil, request.Context().Err()
			}),
		},
	})
	require.NoError(t, err)
	provisioner.vnetClient = clientFactory.NewVirtualNetworksClient()

	allowed, err := provisioner.CheckSubnetCapacity(ctx, buildExpectedSubnetInfo(), expectedSku, expectedClusterSize)

	require.Error(t, err)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.Equal(t, codes.Canceled, status.Code(err))
	assert.False(t, allowed)
	assert.Equal(t, 1, requests, "cancellation during the usage request must not take the permission fallback")
	assert.Equal(t, []string{"ManagementServerTransport.GetRequiredAmlFSSubnetsSize"}, recorder.fakeCallCount)
}
