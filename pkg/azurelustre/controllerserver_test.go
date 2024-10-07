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
	"sort"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TODO_JUSJIN: update and add tests

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
	sort.Slice(capabilitiesSupported,
		func(i, j int) bool {
			return capabilitiesSupported[i] < capabilitiesSupported[j]
		})
	capabilitiesWanted := controllerServiceCapabilities
	sort.Slice(capabilitiesWanted,
		func(i, j int) bool {
			return capabilitiesWanted[i] < capabilitiesWanted[j]
		})
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

func TestCreateVolume_Success_CapacityRoundUp(t *testing.T) {
	capacityInputs := []int64{
		0, laaSOBlockSize - 1, laaSOBlockSize, laaSOBlockSize + 1,
	}
	expectedOutputs := []int64{
		defaultSize, laaSOBlockSize, laaSOBlockSize, laaSOBlockSize * 2,
	}

	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	for idx, capacityInput := range capacityInputs {
		req.CapacityRange = &csi.CapacityRange{
			RequiredBytes: capacityInput,
		}
		rep, err := d.CreateVolume(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, expectedOutputs[idx], rep.GetVolume().GetCapacityBytes())
	}
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

func TestCreateVolume_Err_ParametersNoIP(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	delete(req.GetParameters(), VolumeContextMGSIPAddress)
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "mgs-ip-address")
}

func TestCreateVolume_Err_ParametersEmptyIP(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.Parameters[VolumeContextMGSIPAddress] = ""
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "mgs-ip-address")
}

func TestCreateVolume_Err_ParametersNoFSName(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	delete(req.GetParameters(), VolumeContextFSName)
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "fs-name")
}

func TestCreateVolume_Err_ParametersEmptyFSName(t *testing.T) {
	d := NewFakeDriver()
	req := buildCreateVolumeRequest()
	req.GetParameters()[VolumeContextFSName] = ""
	_, err := d.CreateVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "fs-name")
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
		supported := false
		for _, supportedCapability := range volumeCapabilities {
			if csi.VolumeCapability_AccessMode_Mode(capability) ==
				supportedCapability {
				supported = true
				break
			}
		}
		if !supported {
			capabilitiesNotSupported = append(capabilitiesNotSupported,
				csi.VolumeCapability_AccessMode_Mode(capability))
		}
	}
	if len(capabilitiesNotSupported) != 0 {
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
	} else {
		t.Log("No unsupported AccessMode.")
		assert.True(t, true)
	}
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
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"testVolume", "testFs", "127.0.0.1", "testSubDir"),
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.NoError(t, err)
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
	require.ErrorContains(t, err, "Volume ID")
}

func TestDeleteVolume_Err_HasSecrets(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"testVolume", "testFs", "127.0.0.1", "testSubDir"),
		Secrets: map[string]string{},
	}
	_, err := d.DeleteVolume(context.Background(), req)
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, grpcStatus.Code())
	require.ErrorContains(t, err, "secrets")
}

func TestDeleteVolume_Err_HasSecretsValue(t *testing.T) {
	d := NewFakeDriver()
	req := &csi.DeleteVolumeRequest{
		VolumeId: fmt.Sprintf(volumeIDTemplate,
			"testVolume", "testFs", "127.0.0.1", "testSubDir"),
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
			"testVolume", "testFs", "127.0.0.1", "testSubDir"),
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
			"test", "testFs", "127.0.0.1", "testSubDir"),
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
			"test", "testFs", "127.0.0.1", "testSubDir"),
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
			"test", "testFs", "127.0.0.1", "testSubDir"),
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
			"test", "testFs", "127.0.0.1", "testSubDir"),
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
			"test", "testFs", "127.0.0.1", "testSubDir"),
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
			"test", "testFs", "127.0.0.1", "testSubDir"),
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
		supported := false
		for _, supportedCapability := range volumeCapabilities {
			if csi.VolumeCapability_AccessMode_Mode(capability) ==
				supportedCapability {
				supported = true
				break
			}
		}
		if !supported {
			capabilitiesNotSupported = append(capabilitiesNotSupported,
				csi.VolumeCapability_AccessMode_Mode(capability))
		}
	}
	if len(capabilitiesNotSupported) != 0 {
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
				"test", "testFs", "127.0.0.1", "testSubDir"),
			VolumeCapabilities: capabilities,
		}

		res, err := d.ValidateVolumeCapabilities(context.Background(), req)
		require.NoError(t, err)
		assert.Nil(t, res.GetConfirmed())
	} else {
		t.Log("No unsupported AccessMode.")
		assert.True(t, true)
	}
}
