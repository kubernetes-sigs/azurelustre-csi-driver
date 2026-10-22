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
	"testing"
	"time"

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
