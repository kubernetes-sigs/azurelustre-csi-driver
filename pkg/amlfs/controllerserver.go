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

package amlfs

import (
	"context"
	"fmt"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"k8s.io/klog/v2"

	"sigs.k8s.io/cloud-provider-azure/pkg/metrics"
)

const (
	volumeContextMDSIPAddress = "mds-ip-address"
	volumeContextFSName       = "fs-name"
	defaultSize               = 32000000000000
)

// CreateVolume provisions a volume
func (d *Driver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if err := d.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		klog.Errorf("invalid create volume req: %v", req)
		return nil, err
	}

	volumeCapabilities := req.GetVolumeCapabilities()
	volName := req.GetName()
	if len(volName) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Name must be provided")
	}
	if len(volumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Volume capabilities must be provided")
	}

	if acquired := d.volumeLocks.TryAcquire(volName); !acquired {
		return nil, status.Errorf(codes.Aborted, volumeOperationAlreadyExistsFmt, volName)
	}
	defer d.volumeLocks.Release(volName)

	// TODO_JUSJIN: this should be rounded up to amlfs unit size for real dynamic provisioning
	// TODO_JUSJIN: check req.GetCapacityRange() for real dynamic provisioning
	// TODO_JUSJIN: check other optional parameters to block unsupported requests, e.g. volume_content_source

	parameters := req.GetParameters()
	if parameters == nil {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Parameters must be provided")
	}

	mdsIPAddress, found := parameters[volumeContextMDSIPAddress]
	if !found {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Parameter mds-ip-address must be provided")
	}

	amlFSName, found := parameters[volumeContextFSName]
	if !found {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Parameter fs-name must be provided")
	}

	mc := metrics.NewMetricContext(amlfsCSIDriverName, "controller_create_volume", "<unknown>", "<unknown>", d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	volumeID := fmt.Sprintf(volumeIDTemplate, amlFSName, mdsIPAddress, strconv.Itoa(defaultSize))

	klog.V(2).Infof("begin to create volume(%s) on mds-ip-address(%s) fs-name(%s) size(%d)", mdsIPAddress, amlFSName, defaultSize)

	// TODO_JUSJIN: implement CreateVolume logic for real dynamic provisioning here

	klog.V(2).Infof("created volume(%s) on mds-ip-address(%s) fs-name(%s) size(%d) successfully", mdsIPAddress, amlFSName, defaultSize)

	isOperationSucceeded = true

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: defaultSize,
			VolumeContext: parameters,
		},
	}, nil
}

// DeleteVolume delete a volume
func (d *Driver) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	if err := d.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		return nil, fmt.Errorf("invalid delete volume req: %v", req)
	}

	if acquired := d.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, volumeOperationAlreadyExistsFmt, volumeID)
	}
	defer d.volumeLocks.Release(volumeID)

	mc := metrics.NewMetricContext(amlfsCSIDriverName, "controller_delete_volume", "<unknown>", "<unknown>", d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof("deleting volumeID(%s)", volumeID)

	// TODO_JUSJIN: implement DeleteVolume logic for real dynamic provisioning here

	isOperationSucceeded = true
	klog.V(2).Infof("volumeID(%s) is deleted successfully", volumeID)
	return &csi.DeleteVolumeResponse{}, nil
}

// ValidateVolumeCapabilities return the capabilities of the volume
func (d *Driver) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if req.GetVolumeCapabilities() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities missing in request")
	}

	// TODO_JUSJIN: does it really mean SUPPORTED to caller???
	// amlfs driver supports all AccessModes, no need to check capabilities here
	return &csi.ValidateVolumeCapabilitiesResponse{Message: ""}, nil
}

// ControllerGetCapabilities returns the capabilities of the Controller plugin
func (d *Driver) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: d.Cap,
	}, nil
}
