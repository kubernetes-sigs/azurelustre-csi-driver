/*
Copyright 2020 The Kubernetes Authors.

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
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/util"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

func TestControllerGetCapabilities(t *testing.T) {
	d := NewFakeDriver()
	d.AddControllerServiceCapabilities(controllerServiceCapabilities)
	req := csi.ControllerGetCapabilitiesRequest{}
	resp, err := d.ControllerGetCapabilities(context.Background(), &req)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	capabilitiesSupported := make([]csi.ControllerServiceCapability_RPC_Type, 0, len(resp.GetCapabilities()))
	for _, capabilitySupported := range resp.GetCapabilities() {
		capabilitiesSupported = append(capabilitiesSupported, capabilitySupported.GetRpc().GetType())
	}
	slices.Sort(capabilitiesSupported)
	capabilitiesWanted := controllerServiceCapabilities
	slices.Sort(capabilitiesWanted)
	assert.Equal(t, capabilitiesWanted, capabilitiesSupported)
}

func buildCreateVolumeRequest() *csi.CreateVolumeRequest {
	req := &csi.CreateVolumeRequest{
		Name: "test_volume",
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
				},
			},
		},
		Parameters: map[string]string{
			"fs-name":        "tfs",
			"mgs-ip-address": "127.0.0.1",
			"sub-dir":        "testSubDir",
		},
	}
	return req
}

func buildDynamicProvCreateVolumeRequest() *csi.CreateVolumeRequest {
	req := &csi.CreateVolumeRequest{
		Name: "test_volume",
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
				},
			},
		},
		Parameters: map[string]string{
			"resource-group-name":              "test-resource-group",
			"location":                         "test-location",
			"vnet-resource-group":              "test-vnet-rg",
			"vnet-name":                        "test-vnet-name",
			"subnet-name":                      "test-subnet-name",
			"maintenance-day-of-week":          "Monday",
			"maintenance-time-of-day-utc":      "12:00",
			"sku-name":                         "AMLFS-Durable-Premium-250",
			"identities":                       "identity1,identity2",
			"tags":                             "key1=value1,key2=value2",
			"zones":                            "zone1",
			"sub-dir":                          "testSubDir",
			"csi.storage.k8s.io/pvc/name":      "pvc_name",
			"csi.storage.k8s.io/pvc/namespace": "pvc_namespace",
			"csi.storage.k8s.io/pv/name":       "pv_name",
		},
	}
	return req
}

func TestCreateVolume_Success(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	rep, err := d.CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, rep.GetVolume())
	assert.NotEmpty(t, rep.GetVolume().GetVolumeId())
	assert.NotZero(t, rep.GetVolume().GetCapacityBytes())
	assert.NotEmpty(t, rep.GetVolume().GetVolumeContext())
}

func TestCreateVolume_Success_DoesNotCallDynamicProvisioner(t *testing.T) {
	d := NewFakeDriver()
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner
	req := buildCreateVolumeRequest()
	_, err := d.CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, fakeDynamicProvisioner.Filesystems, 0)
	assert.Empty(t, fakeDynamicProvisioner.fakeCallCount, 0, "unexpected calls made to dynamic provisioner, all calls: %#v", fakeDynamicProvisioner.fakeCallCount)
}

func TestDynamicCreateVolume_Success(t *testing.T) {
	d := NewFakeDriver()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.cloud = azure.GetTestCloud(ctrl)
	req := buildDynamicProvCreateVolumeRequest()
	rep, err := d.CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, rep.GetVolume())
	assert.NotEmpty(t, rep.GetVolume().GetVolumeId())
	assert.NotZero(t, rep.GetVolume().GetCapacityBytes())
	assert.NotEmpty(t, rep.GetVolume().GetVolumeContext())
}

func TestDynamicCreateVolume_Success_SendsCorrectProperties(t *testing.T) {
	expectedAmlfsProperties := &AmlFilesystemProperties{
		ResourceGroupName:    "test-resource-group",
		AmlFilesystemName:    "test_volume",
		Location:             "test-location",
		MaintenanceDayOfWeek: "Monday",
		TimeOfDayUTC:         "12:00",
		SKUName:              "AMLFS-Durable-Premium-250",
		StorageCapacityTiB:   8,
		Identities:           []string{"identity1", "identity2"},
		Tags: map[string]string{
			"key1":                               "value1",
			"key2":                               "value2",
			"k8s-azure-created-by":               "kubernetes-azurelustre-csi-driver",
			"kubernetes.io-created-for-pvc-name": "pvc_name",
			"kubernetes.io-created-for-pv-name":  "pv_name",
			"kubernetes.io-created-for-pvc-namespace": "pvc_namespace",
		},
		Zones: []string{"zone1"},
		SubnetInfo: SubnetProperties{
			VnetResourceGroup: "test-vnet-rg",
			VnetName:          "test-vnet-name",
			SubnetName:        "test-subnet-name",
			SubnetID:          "/subscriptions/subscription/resourceGroups/test-vnet-rg/providers/Microsoft.Network/virtualNetworks/test-vnet-name/subnets/test-subnet-name",
		},
	}

	d := NewFakeDriver()
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.cloud = azure.GetTestCloud(ctrl)
	req := buildDynamicProvCreateVolumeRequest()
	rep, err := d.CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, rep.GetVolume())
	assert.NotEmpty(t, rep.GetVolume().GetVolumeId())
	assert.NotZero(t, rep.GetVolume().GetCapacityBytes())
	assert.NotEmpty(t, rep.GetVolume().GetVolumeContext())
	require.Len(t, fakeDynamicProvisioner.Filesystems, 1)
	require.Len(t, fakeDynamicProvisioner.fakeCallCount, 2, "unexpected calls made to dynamic provisioner, all calls: %#v", fakeDynamicProvisioner.fakeCallCount)
	require.Equal(t, 1, fakeDynamicProvisioner.fakeCallCount["CreateAmlFilesystem"])
	require.Equal(t, 1, fakeDynamicProvisioner.fakeCallCount["GetSkuValuesForLocation"])
	assert.Equal(t, expectedAmlfsProperties, fakeDynamicProvisioner.Filesystems[0])
}

func TestDynamicCreateVolume_Success_DefaultLocation(t *testing.T) {
	d := NewFakeDriver()
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.cloud = azure.GetTestCloud(ctrl)
	req := buildDynamicProvCreateVolumeRequest()
	delete(req.GetParameters(), "location")
	rep, err := d.CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, rep.GetVolume())
	assert.NotEmpty(t, rep.GetVolume().GetVolumeId())
	require.NotEmpty(t, fakeDynamicProvisioner.Filesystems)
	assert.Equal(t, d.location, fakeDynamicProvisioner.Filesystems[0].Location)
}

func TestDynamicCreateVolume_Success_DefaultResourceGroup(t *testing.T) {
	d := NewFakeDriver()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.cloud = azure.GetTestCloud(ctrl)
	req := buildDynamicProvCreateVolumeRequest()
	delete(req.GetParameters(), "resource-group-name")
	rep, err := d.CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, rep.GetVolume())
	assert.NotEmpty(t, rep.GetVolume().GetVolumeId())
	assert.Contains(t, rep.GetVolume().GetVolumeId(), d.resourceGroup)
}

func TestDynamicCreateVolume_Success_UsesReturnedIPAddress(t *testing.T) {
	d := NewFakeDriver()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.cloud = azure.GetTestCloud(ctrl)
	req := buildDynamicProvCreateVolumeRequest()
	delete(req.GetParameters(), "resource-group-name")
	rep, err := d.CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, rep.GetVolume())
	assert.NotEmpty(t, rep.GetVolume().GetVolumeId())
	assert.Contains(t, rep.GetVolume().GetVolumeId(), "127.0.0.2")
}

func TestCreateVolume_Success_CapacityRoundUp(t *testing.T) {
	defaultLaaSOBlockSizeInBytes := int64(defaultLaaSOBlockSizeInTib) * util.TiB

	testCases := []struct {
		desc     string
		capacity int64
		expected int64
	}{
		{
			desc:     "round 0 to default size",
			capacity: 0,
			expected: defaultSizeInBytes,
		},
		{
			desc:     "round block size - 1 to next block size",
			capacity: defaultLaaSOBlockSizeInBytes - 1,
			expected: defaultLaaSOBlockSizeInBytes,
		},
		{
			desc:     "remains at exact block size",
			capacity: defaultLaaSOBlockSizeInBytes,
			expected: defaultLaaSOBlockSizeInBytes,
		},
		{
			desc:     "round block size + 1 to next block size",
			capacity: defaultLaaSOBlockSizeInBytes + 1,
			expected: defaultLaaSOBlockSizeInBytes * 2,
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			d := NewFakeDriver()
			req := buildCreateVolumeRequest()

			req.CapacityRange = &csi.CapacityRange{
				RequiredBytes: tC.capacity,
			}
			rep, err := d.CreateVolume(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, tC.expected, rep.GetVolume().GetCapacityBytes())
		})
	}
}

func TestDynamicCreateVolume_Err_CreateError(t *testing.T) {
	d := NewFakeDriver()
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.cloud = azure.GetTestCloud(ctrl)
	req := buildDynamicProvCreateVolumeRequest()
	req.Name = clusterRequestFailureName
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "error when creating AMLFS")
	require.ErrorContains(t, err, clusterRequestFailureName)
}

func TestDynamicCreateVolume_Err_VolNameTooLong(t *testing.T) {
	d := NewFakeDriver()
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner

	tooLongName := strings.Repeat("a", 81)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.cloud = azure.GetTestCloud(ctrl)
	req := buildDynamicProvCreateVolumeRequest()
	req.Name = tooLongName
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "invalid volume name")
	require.ErrorContains(t, err, tooLongName)
}

func TestCreateVolume_Err_NoName(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.Name = ""
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "Name")
}

func TestCreateVolume_Err_NoVolumeCapabilities(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.VolumeCapabilities = nil
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "Volume capabilities")
}

func TestCreateVolume_Err_EmptyVolumeCapabilities(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.VolumeCapabilities = []*csi.VolumeCapability{}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "Volume capabilities")
}

func TestCreateVolume_Err_NoParameters(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.Parameters = nil
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "Parameters must be provided")
}

func TestCreateVolume_Err_BadSku(t *testing.T) {
	d := NewFakeDriver()
	req := buildDynamicProvCreateVolumeRequest()
	req.Parameters["sku-name"] = "bad-sku"
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "sku-name must be one of")
}

func TestCreateVolume_Err_CapacityAboveSkuMax(t *testing.T) {
	d := NewFakeDriver()
	req := buildDynamicProvCreateVolumeRequest()
	req.CapacityRange = &csi.CapacityRange{
		RequiredBytes: 9000 * util.TiB,
	}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "exceeds maximum capacity")
}

func TestCreateVolume_Err_CapacityOverflow(t *testing.T) {
	d := NewFakeDriver()
	req := buildDynamicProvCreateVolumeRequest()
	req.CapacityRange = &csi.CapacityRange{
		RequiredBytes: math.MaxInt64,
	}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "value overflow")
}

func TestCreateVolume_Err_CapacityAboveLimit(t *testing.T) {
	d := NewFakeDriver()
	req := buildDynamicProvCreateVolumeRequest()
	req.CapacityRange = &csi.CapacityRange{
		RequiredBytes: 2 * util.TiB,
		LimitBytes:    1 * util.TiB,
	}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "greater than capacity limit")
}

func TestCreateVolume_Success_NoFSName(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	delete(req.GetParameters(), VolumeContextFSName)
	rep, err := d.CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, rep.GetVolume())
	expectedOutput := "test_volume#lustrefs#127.0.0.1#testSubDir#f#"
	assert.Equal(t, expectedOutput, rep.GetVolume().GetVolumeId())
}

func TestCreateVolume_Success_EmptyFSName(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.GetParameters()[VolumeContextFSName] = ""
	rep, err := d.CreateVolume(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, rep.GetVolume())
	expectedOutput := "test_volume#lustrefs#127.0.0.1#testSubDir#f#"
	assert.Equal(t, expectedOutput, rep.GetVolume().GetVolumeId())
}

func TestCreateVolume_Err_ParametersEmptySubDir(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.Parameters[VolumeContextSubDir] = ""
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "sub-dir")
}

func TestCreateVolume_Err_UnknownParameters(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.Parameters["FirstNonexistentParameter"] = "Invalid"
	req.Parameters["AnotherNonexistentParameter"] = "Invalid"
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "Invalid parameter")
	require.ErrorContains(t, err, "FirstNonexistentParameter")
	require.ErrorContains(t, err, "AnotherNonexistentParameter")
}

func TestCreateVolume_Err_UnknownParametersDynamicProvisioning(t *testing.T) {
	d := NewFakeDriver()
	req := buildDynamicProvCreateVolumeRequest()
	req.Parameters["FirstNonexistentParameter"] = "Invalid"
	req.Parameters["AnotherNonexistentParameter"] = "Invalid"
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "Invalid parameter")
	require.ErrorContains(t, err, "FirstNonexistentParameter")
	require.ErrorContains(t, err, "AnotherNonexistentParameter")
}

func TestCreateVolume_Err_HasVolumeContentSource(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.VolumeContentSource = &csi.VolumeContentSource{}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "existing volume")
}

func TestCreateVolume_Err_HasSecrets(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.Secrets = map[string]string{}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "secrets")
}

func TestCreateVolume_Err_HasSecretsValue(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.Secrets = map[string]string{"test": "test"}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "secrets")
}

func TestCreateVolume_Err_HasAccessibilityRequirements(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.AccessibilityRequirements = &csi.TopologyRequirement{}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "accessibility_requirements")
}

func TestCreateVolume_Err_BlockVolume(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.VolumeCapabilities = []*csi.VolumeCapability{
		{
			AccessType: &csi.VolumeCapability_Block{
				Block: &csi.VolumeCapability_BlockVolume{},
			},
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
			},
		},
	}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "block volume")
}

func TestCreateVolume_Err_BlockMountVolume(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.VolumeCapabilities = append(req.VolumeCapabilities,
		&csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Block{
				Block: &csi.VolumeCapability_BlockVolume{},
			},
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: volumeCapabilities[0],
			},
		})
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "block volume")
}

func TestCreateVolume_Err_NotSupportedAccessMode(t *testing.T) {
	capabilitiesNotSupported := []csi.VolumeCapability_AccessMode_Mode{}
	for capability := range csi.VolumeCapability_AccessMode_Mode_name {
		supported := slices.Contains(volumeCapabilities, csi.VolumeCapability_AccessMode_Mode(capability))
		if !supported {
			capabilitiesNotSupported = append(capabilitiesNotSupported,
				csi.VolumeCapability_AccessMode_Mode(capability))
		}
	}

	require.NotEmpty(t, capabilitiesNotSupported, "No unsupported AccessMode.")

	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.VolumeCapabilities = []*csi.VolumeCapability{}
	t.Logf("Unsupported access modes: %s", capabilitiesNotSupported)
	for _, capabilityNotSupported := range capabilitiesNotSupported {
		req.VolumeCapabilities = append(req.VolumeCapabilities,
			&csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: capabilityNotSupported,
				},
			},
		)
	}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, capabilitiesNotSupported[0].String())
}

func TestCreateVolume_Err_OperationExists(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	if acquired := d.volumeLocks.TryAcquire(req.GetName()); !acquired {
		assert.Fail(t, "Can't acquire volume lock")
	}
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Aborted, grpcStatus.Code())
	assert.Regexp(t, "operation.*already exists", err.Error())
}

func TestDeleteVolume_Success(t *testing.T) {
	d := NewFakeDriver()
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test_volume", "testFs", "127.0.0.1", "testSubDir", "f", ""),
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.Empty(t, fakeDynamicProvisioner.fakeCallCount, "unexpected calls made to dynamic provisioner, all calls: %#v", fakeDynamicProvisioner.fakeCallCount)
	require.NoError(t, err)
}

func TestDeleteVolume_Success_MissingDynamicCreateValue(t *testing.T) {
	d := NewFakeDriver()
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test_volume", "testFs", "127.0.0.1", "testSubDir", "", ""),
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.Empty(t, fakeDynamicProvisioner.fakeCallCount, "unexpected calls made to dynamic provisioner, all calls: %#v", fakeDynamicProvisioner.fakeCallCount)
	require.NoError(t, err)
}

func TestDeleteVolume_Success_UnnecessaryResourceGroup(t *testing.T) {
	d := NewFakeDriver()
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test_volume", "testFs", "127.0.0.1", "testSubDir", "f", "testResourceGroupName"),
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.Empty(t, fakeDynamicProvisioner.fakeCallCount, "unexpected calls made to dynamic provisioner, all calls: %#v", fakeDynamicProvisioner.fakeCallCount)
	require.NoError(t, err)
}

func TestDeleteVolume_Success_NoFsName(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"testVolume", "", "127.0.0.1", "testSubDir", "f", ""),
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.NoError(t, err)
}

func TestDynamicDeleteVolume_Success(t *testing.T) {
	d := NewFakeDriver()
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.cloud = azure.GetTestCloud(ctrl)
	createReq := buildDynamicProvCreateVolumeRequest()
	rep, err := d.CreateVolume(context.Background(), createReq)
	require.NoError(t, err)
	assert.NotEmpty(t, rep.GetVolume())
	assert.NotEmpty(t, rep.GetVolume().GetVolumeId())
	require.NotEmpty(t, fakeDynamicProvisioner.Filesystems)
	fakeDynamicProvisioner.fakeCallCount = make(map[string]int)

	deleteRequest := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test_volume", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
	}
	_, err = d.DeleteVolume(context.Background(), deleteRequest)
	require.Len(t, fakeDynamicProvisioner.fakeCallCount, 1, "unexpected calls made to dynamic provisioner, all calls: %#v", fakeDynamicProvisioner.fakeCallCount)
	require.Equal(t, 1, fakeDynamicProvisioner.fakeCallCount["DeleteAmlFilesystem"])
	require.NoError(t, err)
	assert.Empty(t, fakeDynamicProvisioner.Filesystems)
}

func TestDynamicDeleteVolume_Err_DeleteError(t *testing.T) {
	d := NewFakeDriver()
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.cloud = azure.GetTestCloud(ctrl)
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			clusterRequestFailureName, "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "error when deleting AMLFS")
	require.ErrorContains(t, err, clusterRequestFailureName)
}

func TestDeleteVolume_Err_NoVolumeID(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.DeleteVolumeRequest{
		VolumeId: "",
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "Volume ID missing")
}

func TestDeleteVolume_Success_InvalidVolumeID(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.DeleteVolumeRequest{
		VolumeId: "#",
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.NoError(t, err)
}

func TestDeleteVolume_Err_HasSecrets(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test_volume", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
		Secrets: map[string]string{},
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "secrets")
}

func TestDynamicDeleteVolume_Err_NoResourceGroup(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test_volume", "testFs", "127.0.0.1", "testSubDir", "t", ""),
	}
	fakeDynamicProvisioner := &FakeDynamicProvisioner{}
	d.dynamicProvisioner = fakeDynamicProvisioner

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.cloud = azure.GetTestCloud(ctrl)
	_, err := d.DeleteVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "volume was dynamically created but associated resource group is not specified")
	require.Empty(t, fakeDynamicProvisioner.fakeCallCount, "unexpected calls made to dynamic provisioner, all calls: %#v", fakeDynamicProvisioner.fakeCallCount)
}

func TestDeleteVolume_Err_HasSecretsValue(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test_volume", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
		Secrets: map[string]string{
			"test": "test",
		},
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "secrets")
}

func TestDeleteVolume_Err_OperationExists(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test_volume", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
	}
	if acquired := d.volumeLocks.TryAcquire(req.GetVolumeId()); !acquired {
		assert.Fail(t, "Can't acquire volume lock")
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Aborted, grpcStatus.Code())
	assert.Regexp(t, "operation.*already exists", err.Error())
}

func TestValidateVolumeCapabilities_Success(t *testing.T) {
	d := NewFakeDriver()
	capabilities := []*csi.VolumeCapability{}
	for _, capability := range volumeCapabilities {
		capabilities = append(
			capabilities,
			&csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: capability,
				},
			},
		)
	}
	req := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
		VolumeCapabilities: capabilities,
	}

	_, err := d.ValidateVolumeCapabilities(context.Background(), req)
	require.NoError(t, err)
}

func TestValidateVolumeCapabilities_Err_NoVolumeID(t *testing.T) {
	d := NewFakeDriver()
	capabilities := []*csi.VolumeCapability{}
	for _, capability := range volumeCapabilities {
		capabilities = append(
			capabilities,
			&csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: capability,
				},
			},
		)
	}
	req := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId:           "",
		VolumeCapabilities: capabilities,
	}

	_, err := d.ValidateVolumeCapabilities(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "Volume ID")
}

func TestValidateVolumeCapabilities_Err_NoVolumeCapabilities(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
		VolumeCapabilities: nil,
	}

	_, err := d.ValidateVolumeCapabilities(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "capabilities")
}

func TestValidateVolumeCapabilities_Err_EmptyVolumeCapabilities(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
		VolumeCapabilities: []*csi.VolumeCapability{},
	}

	_, err := d.ValidateVolumeCapabilities(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "capabilities")
}

func TestValidateVolumeCapabilities_Err_HasSecretes(t *testing.T) {
	d := NewFakeDriver()
	capabilities := []*csi.VolumeCapability{}
	for _, capability := range volumeCapabilities {
		capabilities = append(
			capabilities,
			&csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: capability,
				},
			},
		)
	}
	req := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
		VolumeCapabilities: capabilities,
		Secrets:            map[string]string{},
	}

	_, err := d.ValidateVolumeCapabilities(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "secrets")
}

func TestValidateVolumeCapabilities_Err_HasSecretesValue(t *testing.T) {
	d := NewFakeDriver()
	capabilities := []*csi.VolumeCapability{}
	for _, capability := range volumeCapabilities {
		capabilities = append(
			capabilities,
			&csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: capability,
				},
			},
		)
	}
	req := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
		VolumeCapabilities: capabilities,
		Secrets:            map[string]string{"test": "test"},
	}

	_, err := d.ValidateVolumeCapabilities(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "secrets")
}

func TestValidateVolumeCapabilities_Success_BlockCapabilities(t *testing.T) {
	d := NewFakeDriver()
	capabilities := []*csi.VolumeCapability{}
	for _, capability := range volumeCapabilities {
		capabilities = append(
			capabilities,
			&csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Block{
					Block: &csi.VolumeCapability_BlockVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: capability,
				},
			},
		)
	}
	req := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
		VolumeCapabilities: capabilities,
	}

	res, err := d.ValidateVolumeCapabilities(context.Background(), req)
	require.NoError(t, err)
	assert.Nil(t, res.GetConfirmed())
}

func TestValidateVolumeCapabilities_Success_HasUnsupportedAccessMode(
	t *testing.T,
) {
	capabilitiesNotSupported := []csi.VolumeCapability_AccessMode_Mode{}
	for capability := range csi.VolumeCapability_AccessMode_Mode_name {
		supported := slices.Contains(volumeCapabilities, csi.VolumeCapability_AccessMode_Mode(capability))
		if !supported {
			capabilitiesNotSupported = append(capabilitiesNotSupported,
				csi.VolumeCapability_AccessMode_Mode(capability))
		}
	}

	require.NotEmpty(t, capabilitiesNotSupported, "No unsupported AccessMode.")

	d := NewFakeDriver()
	capabilities := []*csi.VolumeCapability{}
	for _, capability := range capabilitiesNotSupported {
		capabilities = append(
			capabilities,
			&csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Block{
					Block: &csi.VolumeCapability_BlockVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: capability,
				},
			},
		)
	}
	req := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"test", "testFs", "127.0.0.1", "testSubDir", "t", "testResourceGroupName"),
		VolumeCapabilities: capabilities,
	}

	res, err := d.ValidateVolumeCapabilities(context.Background(), req)
	require.NoError(t, err)
	assert.Nil(t, res.GetConfirmed())
}

func TestParseAmlfilesystemProperties_Success(t *testing.T) {
	properties := map[string]string{
		"resource-group-name":         "test-resource-group",
		"location":                    "test-location",
		"vnet-resource-group":         "test-vnet-rg",
		"vnet-name":                   "test-vnet-name",
		"subnet-name":                 "test-subnet-name",
		"maintenance-day-of-week":     "Monday",
		"maintenance-time-of-day-utc": "12:00",
		"sku-name":                    "AMLFS-Durable-Premium-40",
		"identities":                  "identity1,identity2",
		"tags":                        "key1=value1,key2=value2",
		"zones":                       "zone1",
	}

	expected := &AmlFilesystemProperties{
		ResourceGroupName:    "test-resource-group",
		AmlFilesystemName:    "",
		Location:             "test-location",
		MaintenanceDayOfWeek: "Monday",
		TimeOfDayUTC:         "12:00",
		SKUName:              "AMLFS-Durable-Premium-40",
		Identities:           []string{"identity1", "identity2"},
		Tags: map[string]string{
			"key1":       "value1",
			"key2":       "value2",
			createdByTag: azureLustreDriverTag,
		},
		Zones: []string{"zone1"},
		SubnetInfo: SubnetProperties{
			VnetResourceGroup: "test-vnet-rg",
			VnetName:          "test-vnet-name",
			SubnetName:        "test-subnet-name",
		},
	}

	result, err := parseAmlFilesystemProperties(properties)
	require.NoError(t, err)
	assert.Equal(t, expected, result)
}

func TestParseAmlfilesystemProperties_Err_InvalidParameters(t *testing.T) {
	properties := map[string]string{
		"invalid-param":               "invalid",
		"resource-group-name":         "test-resource-group",
		"location":                    "test-location",
		"vnet-resource-group":         "test-vnet-rg",
		"vnet-name":                   "test-vnet-name",
		"subnet-name":                 "test-subnet-name",
		"maintenance-day-of-week":     "Monday",
		"maintenance-time-of-day-utc": "12:00",
		"sku-name":                    "AMLFS-Durable-Premium-40",
		"zones":                       "zone1",
	}

	_, err := parseAmlFilesystemProperties(properties)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "Invalid parameter(s)")
	require.ErrorContains(t, err, "invalid-param")
}

func TestParseAmlfilesystemProperties_Err_OnlySingleZoneIsSupported(t *testing.T) {
	properties := map[string]string{
		"resource-group-name":         "test-resource-group",
		"location":                    "test-location",
		"vnet-resource-group":         "test-vnet-rg",
		"vnet-name":                   "test-vnet-name",
		"subnet-name":                 "test-subnet-name",
		"maintenance-time-of-day-utc": "12:00",
		"sku-name":                    "AMLFS-Durable-Premium-40",
		"zones":                       "zone1,zone2",
	}

	_, err := parseAmlFilesystemProperties(properties)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "single zone")
}

func TestParseAmlfilesystemProperties_Err_MissingMaintenanceDayOfWeek(t *testing.T) {
	properties := map[string]string{
		"resource-group-name":         "test-resource-group",
		"location":                    "test-location",
		"vnet-resource-group":         "test-vnet-rg",
		"vnet-name":                   "test-vnet-name",
		"subnet-name":                 "test-subnet-name",
		"maintenance-time-of-day-utc": "12:00",
		"sku-name":                    "AMLFS-Durable-Premium-40",
		"zones":                       "zone1",
	}

	_, err := parseAmlFilesystemProperties(properties)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "maintenance-day-of-week must be provided")
}

func TestParseAmlfilesystemProperties_Err_EmptyMaintenanceDayOfWeek(t *testing.T) {
	properties := map[string]string{
		"resource-group-name":         "test-resource-group",
		"location":                    "test-location",
		"vnet-resource-group":         "test-vnet-rg",
		"vnet-name":                   "test-vnet-name",
		"subnet-name":                 "test-subnet-name",
		"maintenance-day-of-week":     "",
		"maintenance-time-of-day-utc": "12:00",
		"sku-name":                    "AMLFS-Durable-Premium-40",
		"zones":                       "zone1",
	}

	_, err := parseAmlFilesystemProperties(properties)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "maintenance-day-of-week must be one of")
}

func TestParseAmlfilesystemProperties_Err_InvalidMaintenanceDayOfWeek(t *testing.T) {
	properties := map[string]string{
		"resource-group-name":         "test-resource-group",
		"location":                    "test-location",
		"vnet-resource-group":         "test-vnet-rg",
		"vnet-name":                   "test-vnet-name",
		"subnet-name":                 "test-subnet-name",
		"maintenance-day-of-week":     "invalid-day-of-week",
		"maintenance-time-of-day-utc": "12:00",
		"sku-name":                    "AMLFS-Durable-Premium-40",
		"zones":                       "zone1",
	}

	_, err := parseAmlFilesystemProperties(properties)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "maintenance-day-of-week must be one of")
}

func TestParseAmlfilesystemProperties_Err_MissingTimeOfDay(t *testing.T) {
	properties := map[string]string{
		"resource-group-name":     "test-resource-group",
		"location":                "test-location",
		"vnet-resource-group":     "test-vnet-rg",
		"vnet-name":               "test-vnet-name",
		"subnet-name":             "test-subnet-name",
		"maintenance-day-of-week": "Monday",
		"sku-name":                "AMLFS-Durable-Premium-40",
		"zones":                   "zone1",
	}

	_, err := parseAmlFilesystemProperties(properties)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "maintenance-time-of-day-utc must be provided")
}

func TestParseAmlfilesystemProperties_Err_MissingSku(t *testing.T) {
	properties := map[string]string{
		"resource-group-name":         "test-resource-group",
		"location":                    "test-location",
		"vnet-resource-group":         "test-vnet-rg",
		"vnet-name":                   "test-vnet-name",
		"subnet-name":                 "test-subnet-name",
		"maintenance-day-of-week":     "Monday",
		"maintenance-time-of-day-utc": "12:00",
		"zones":                       "zone1",
	}

	_, err := parseAmlFilesystemProperties(properties)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "sku-name must be provided")
}

func TestParseAmlfilesystemProperties_Err_InvalidTimeOfDay(t *testing.T) {
	properties := map[string]string{
		"resource-group-name":         "test-resource-group",
		"location":                    "test-location",
		"vnet-resource-group":         "test-vnet-rg",
		"vnet-name":                   "test-vnet-name",
		"subnet-name":                 "test-subnet-name",
		"maintenance-day-of-week":     "Monday",
		"maintenance-time-of-day-utc": "11",
		"sku-name":                    "AMLFS-Durable-Premium-40",
		"zones":                       "zone1",
	}

	_, err := parseAmlFilesystemProperties(properties)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "maintenance-time-of-day-utc must be in the form HH:MM")
}

func TestParseAmlfilesystemProperties_Err_MissingZones(t *testing.T) {
	properties := map[string]string{
		"resource-group-name":         "test-resource-group",
		"location":                    "test-location",
		"vnet-resource-group":         "test-vnet-rg",
		"vnet-name":                   "test-vnet-name",
		"subnet-name":                 "test-subnet-name",
		"maintenance-day-of-week":     "Monday",
		"maintenance-time-of-day-utc": "12:00",
		"sku-name":                    "AMLFS-Durable-Premium-40",
	}

	_, err := parseAmlFilesystemProperties(properties)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "zones must be provided")
}

func TestParseAmlfilesystemProperties_Err_InvalidTags(t *testing.T) {
	properties := map[string]string{
		"resource-group-name":         "test-resource-group",
		"location":                    "test-location",
		"vnet-resource-group":         "test-vnet-rg",
		"vnet-name":                   "test-vnet-name",
		"subnet-name":                 "test-subnet-name",
		"maintenance-day-of-week":     "Monday",
		"maintenance-time-of-day-utc": "12:00",
		"sku-name":                    "AMLFS-Durable-Premium-40",
		"zones":                       "zone1",
		"tags":                        "key1:value1,=value2",
	}

	_, err := parseAmlFilesystemProperties(properties)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "are invalid, the format should be: 'key1=value1,key2=value2")
}

func TestParseAmlfilesystemProperties_Err_ReservedTags(t *testing.T) {
	testCases := []struct {
		reservedTag string
	}{
		{
			reservedTag: createdByTag,
		},
		{
			reservedTag: pvNameTag,
		},
		{
			reservedTag: pvcNameTag,
		},
		{
			reservedTag: pvcNamespaceTag,
		},
	}
	for _, tC := range testCases {
		properties := map[string]string{
			"resource-group-name":         "test-resource-group",
			"location":                    "test-location",
			"vnet-resource-group":         "test-vnet-rg",
			"vnet-name":                   "test-vnet-name",
			"subnet-name":                 "test-subnet-name",
			"maintenance-day-of-week":     "Monday",
			"maintenance-time-of-day-utc": "12:00",
			"sku-name":                    "AMLFS-Durable-Premium-40",
			"zones":                       "zone1",
		}

		t.Run(tC.reservedTag, func(t *testing.T) {
			properties["tags"] = fmt.Sprintf("key1=value1,%s=value2", tC.reservedTag)
			_, err := parseAmlFilesystemProperties(properties)
			require.Error(t, err)
			grpcStatus, ok := status.FromError(err)
			assert.True(t, ok)
			assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
			require.ErrorContains(t, err, "must not contain "+tC.reservedTag+" as a tag")
		})
	}
}

func TestValidateVolumeName(t *testing.T) {
	tests := []struct {
		desc              string
		amlFilesystemName string
		volumeName        string
		expected          bool
	}{
		{
			desc:       "valid alpha name",
			volumeName: "aqz",
			expected:   true,
		},
		{
			desc:       "valid numeric name",
			volumeName: "029",
			expected:   true,
		},
		{
			desc:       "max length name",
			volumeName: strings.Repeat("a", amlFilesystemNameMaxLength),
			expected:   true,
		},
		{
			desc:       "name too long",
			volumeName: strings.Repeat("a", amlFilesystemNameMaxLength+1),
			expected:   false,
		},
		{
			desc:       "removes invalid characters when using default name",
			volumeName: "-!@#$*amlfsName%@#$",
			expected:   false,
		},
		{
			desc:       "removes non alphanumeric at beginning when using default name",
			volumeName: "#",
			expected:   false,
		},
		{
			desc:       "truncates when using default name",
			volumeName: "#",
			expected:   false,
		},
		{
			desc:       "invalid end character",
			volumeName: "aq-",
			expected:   false,
		},
		{
			desc:       "invalid start character",
			volumeName: "-aq",
			expected:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			result := isValidVolumeName(test.volumeName)
			assert.Equal(t, test.expected, result, "input: %q, getValidAmlFilesystemName result: %q, expected: %q", test.amlFilesystemName, result, test.expected)
		})
	}
}

func TestRoundToAmlfsBlockSizeForSku(t *testing.T) {
	d := NewFakeDriver()
	laaSOBlockSizeInBytes := int64(defaultLaaSOBlockSizeInTib) * util.TiB

	tests := []struct {
		desc               string
		capacityInBytes    int64
		blockSizeInBytes   int64
		maxCapacityInBytes int64
		expected           int64
		expectError        bool
	}{
		{
			desc:        "default size",
			expected:    defaultSizeInBytes,
			expectError: false,
		},
		{
			desc:             "round up to block size",
			capacityInBytes:  laaSOBlockSizeInBytes - 1,
			blockSizeInBytes: laaSOBlockSizeInBytes,
			expected:         laaSOBlockSizeInBytes,
			expectError:      false,
		},
		{
			desc:             "exact block size",
			capacityInBytes:  laaSOBlockSizeInBytes,
			blockSizeInBytes: laaSOBlockSizeInBytes,
			expected:         laaSOBlockSizeInBytes,
			expectError:      false,
		},
		{
			desc:             "round up to next block size",
			capacityInBytes:  laaSOBlockSizeInBytes + 1,
			blockSizeInBytes: laaSOBlockSizeInBytes,
			expected:         2 * laaSOBlockSizeInBytes,
			expectError:      false,
		},
		{
			desc:             "round up to larger block size",
			capacityInBytes:  2*laaSOBlockSizeInBytes - 1,
			blockSizeInBytes: laaSOBlockSizeInBytes,
			expected:         2 * laaSOBlockSizeInBytes,
			expectError:      false,
		},
		{
			desc:               "exceeds maximum capacity",
			capacityInBytes:    2 * laaSOBlockSizeInBytes,
			blockSizeInBytes:   laaSOBlockSizeInBytes,
			maxCapacityInBytes: laaSOBlockSizeInBytes,
			expected:           0,
			expectError:        true,
		},
		{
			desc:             "capacity overflow",
			capacityInBytes:  math.MaxInt64,
			blockSizeInBytes: math.MaxInt64 - 1,
			expected:         0,
			expectError:      true,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			result, err := d.roundToAmlfsBlockSize(test.capacityInBytes, test.blockSizeInBytes, test.maxCapacityInBytes)
			if test.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, test.expected, result)
			}
		})
	}
}
