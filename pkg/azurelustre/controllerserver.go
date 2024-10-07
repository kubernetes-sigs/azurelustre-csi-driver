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
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"k8s.io/klog/v2"

	"sigs.k8s.io/cloud-provider-azure/pkg/metrics"
)

const (
	VolumeContextMGSIPAddress = "mgs-ip-address"
	VolumeContextFSName       = "fs-name"
	VolumeContextSubDir       = "sub-dir"
	defaultSize               = 4 * 1024 * 1024 * 1024 * 1024 // 4TiB
	laaSOBlockSize            = 4 * 1024 * 1024 * 1024 * 1024 // 4TiB
)

func validateVolumeCapabilities(capabilities []*csi.VolumeCapability) error {
	for _, capability := range capabilities {
		if nil == capability.GetMount() {
			// Lustre just support mount type. i.e. block type is unsupported.
			return status.Error(codes.InvalidArgument,
				"Doesn't support block volume.")
		}
		support := false
		for _, supportedCapability := range volumeCapabilities {
			if capability.GetAccessMode().GetMode() == supportedCapability {
				support = true
				break
			}
		}
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
	_ context.Context,
	req *csi.CreateVolumeRequest,
) (*csi.CreateVolumeResponse, error) {
	mc := metrics.NewMetricContext(
		azureLustreCSIDriverName,
		"controller_create_volume",
		"",
		"",
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

	capacityInBytes := req.GetCapacityRange().GetRequiredBytes()
	if 0 == capacityInBytes {
		capacityInBytes = defaultSize
	}

	// round up capacity to next laaSOBlockSize
	capacityInBytes = ((capacityInBytes + laaSOBlockSize - 1) /
		laaSOBlockSize) * laaSOBlockSize

	if acquired := d.volumeLocks.TryAcquire(volName); !acquired {
		return nil, status.Errorf(codes.Aborted,
			volumeOperationAlreadyExistsFmt,
			volName)
	}
	defer d.volumeLocks.Release(volName)

	// TODO_JUSJIN: check req.GetCapacityRange()

	parameters := req.GetParameters()
	if parameters == nil {
		return nil, status.Error(codes.InvalidArgument,
			"CreateVolume Parameters must be provided")
	}

	volumeID, err := createVolumeIDFromParams(volName, parameters)
	if err != nil {
		return nil, err
	}

	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof(
		"begin to create volumeID(%s)", volumeID,
	)

	// TODO_JUSJIN: implement CreateVolume logic for real dynamic provisioning

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

func checkVolumeRequest(req *csi.CreateVolumeRequest) error {
	volumeCapabilities := req.GetVolumeCapabilities()
	if len(volumeCapabilities) == 0 {
		return status.Error(
			codes.InvalidArgument,
			"CreateVolume Volume capabilities must be provided",
		)
	}
	if nil != req.GetVolumeContentSource() {
		return status.Error(
			codes.InvalidArgument,
			"CreateVolume doesn't support being created from an existing volume",
		)
	}
	if nil != req.GetSecrets() {
		return status.Error(
			codes.InvalidArgument,
			"CreateVolume doesn't support secrets",
		)
	}
	if nil != req.GetAccessibilityRequirements() {
		return status.Error(
			codes.InvalidArgument,
			"CreateVolume doesn't support accessibility_requirements",
		)
	}
	capabilityError := validateVolumeCapabilities(volumeCapabilities)
	if nil != capabilityError {
		return capabilityError
	}
	return nil
}

// DeleteVolume delete a volume
func (d *Driver) DeleteVolume(
	_ context.Context, req *csi.DeleteVolumeRequest,
) (*csi.DeleteVolumeResponse, error) {
	mc := metrics.NewMetricContext(azureLustreCSIDriverName,
		"controller_delete_volume",
		"",
		"",
		d.Name)

	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"Volume ID missing in request")
	}
	if nil != req.GetSecrets() {
		return nil, status.Error(
			codes.InvalidArgument,
			"CreateVolume doesn't support secrets",
		)
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

	// TODO_JUSJIN: implement DeleteVolume logic for real dynamic provisioning

	isOperationSucceeded = true
	klog.V(2).Infof("volumeID(%s) is deleted successfully", volumeID)
	return &csi.DeleteVolumeResponse{}, nil
}

// ValidateVolumeCapabilities return the capabilities of the volume
func (d *Driver) ValidateVolumeCapabilities(
	_ context.Context,
	req *csi.ValidateVolumeCapabilitiesRequest,
) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if nil != req.GetSecrets() {
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
	if nil != capabilityError {
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
	var mgsIPAddress, azureLustreName, subDir string

	var errorParameters []string

	// validate parameters (case-insensitive).
	for k, v := range params {
		switch strings.ToLower(k) {
		case VolumeContextMGSIPAddress:
			mgsIPAddress = v
		case VolumeContextFSName:
			azureLustreName = v
		case VolumeContextSubDir:
			subDir = v
			subDir = strings.Trim(subDir, "/")

			if len(subDir) == 0 {
				return "", status.Error(
					codes.InvalidArgument,
					"CreateVolume Parameter sub-dir must not be empty if provided",
				)
			}
		// These will be used by the node methods
		case pvcNamespaceKey:
		case pvcNameKey:
		case pvNameKey:
			continue
		default:
			errorParameters = append(
				errorParameters,
				fmt.Sprintf("%s = %s", k, v),
			)
		}
	}

	if len(mgsIPAddress) == 0 {
		return "", status.Error(
			codes.InvalidArgument,
			"CreateVolume Parameter mgs-ip-address must be provided",
		)
	}

	azureLustreName = strings.Trim(azureLustreName, "/")
	if len(azureLustreName) == 0 {
		return "", status.Error(
			codes.InvalidArgument,
			"CreateVolume Parameter fs-name must be provided",
		)
	}

	if len(errorParameters) > 0 {
		return "", status.Error(
			codes.InvalidArgument,
			fmt.Sprintf("Invalid parameter(s) {%s} in storage class",
				strings.Join(errorParameters, ", ")),
		)
	}

	volumeID := fmt.Sprintf(volumeIDTemplate, volName, azureLustreName, mgsIPAddress, subDir)

	return volumeID, nil
}
