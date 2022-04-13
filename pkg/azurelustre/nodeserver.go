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
	"fmt"
	"io/ioutil"
	"os"

	volumehelper "sigs.k8s.io/azurelustre-csi-driver/pkg/util"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/volume"
	mount "k8s.io/mount-utils"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"golang.org/x/net/context"
)

// NodePublishVolume mount the volume from staging to target path
func (d *Driver) NodePublishVolume(
	ctx context.Context,
	req *csi.NodePublishVolumeRequest,
) (*csi.NodePublishVolumeResponse, error) {
	volCap := req.GetVolumeCapability()
	if volCap == nil {
		return nil, status.Error(codes.InvalidArgument,
			"Volume capability missing in request")
	}
	volumeID := req.GetVolumeId()
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"Volume ID missing in request")
	}

	target := req.GetTargetPath()
	if len(target) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"Target path not provided")
	}

	context := req.GetVolumeContext()
	if context == nil {
		return nil, status.Error(codes.InvalidArgument,
			"Volume context must be provided")
	}

	mdsIPAddress, found := context[VolumeContextMDSIPAddress]
	if !found {
		return nil, status.Error(codes.InvalidArgument,
			"Context mds-ip-address must be provided")
	}

	azureLustreName, found := context[VolumeContextFSName]
	if !found {
		return nil, status.Error(codes.InvalidArgument,
			"Context fs-name must be provided")
	}

	source := fmt.Sprintf("%s@tcp:/%s", mdsIPAddress, azureLustreName)

	mountOptions := []string{}
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	mnt, err := d.ensureMountPoint(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal,
			"Could not mount target %q: %v",
			target,
			err)
	}
	if mnt {
		klog.V(2).Infof(
			"NodePublishVolume: volume %s is already mounted on %s",
			volumeID,
			target,
		)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	klog.V(2).Infof(
		"NodePublishVolume: volume %s mounting %s at %s with mountOptions: %v",
		volumeID, source, target, mountOptions,
	)
	if d.enableAzureLustreMockMount {
		klog.Warningf(
			"NodePublishVolume: mock mount on volumeID(%s), this is only for"+
				"TESTING!!!",
			volumeID,
		)
		if err := volumehelper.MakeDir(target); err != nil {
			klog.Errorf("MakeDir failed on target: %s (%v)", target, err)
			return nil, err
		}
		return &csi.NodePublishVolumeResponse{}, nil
	}

	err = d.mounter.Mount(source, target, "lustre", mountOptions)

	if err != nil {
		if removeErr := os.Remove(target); removeErr != nil {
			return nil, status.Errorf(
				codes.Internal,
				"Could not remove mount target %q: %v",
				target,
				removeErr,
			)
		}
		return nil, status.Errorf(codes.Internal,
			"Could not mount %q at %q: %v", source, target, err)
	}
	klog.V(2).Infof(
		"NodePublishVolume: volume %s mount %s at %s successfully",
		volumeID,
		source,
		target,
	)

	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unmount the volume from the target path
func (d *Driver) NodeUnpublishVolume(
	ctx context.Context,
	req *csi.NodeUnpublishVolumeRequest,
) (*csi.NodeUnpublishVolumeResponse, error) {
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"Volume ID missing in request")
	}
	if len(req.GetTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"Target path missing in request")
	}
	targetPath := req.GetTargetPath()
	volumeID := req.GetVolumeId()

	klog.V(2).Infof("NodeUnpublishVolume: unmounting volume %s on %s",
		volumeID, targetPath)
	err := mount.CleanupMountPoint(targetPath, d.mounter,
		true /*extensiveMountPointCheck*/)
	if err != nil {
		return nil, status.Errorf(codes.Internal,
			"failed to unmount target %q: %v", targetPath, err)
	}
	klog.V(2).Infof(
		"NodeUnpublishVolume: unmount volume %s on %s successfully",
		volumeID,
		targetPath,
	)

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeGetCapabilities return the capabilities of the Node plugin
func (d *Driver) NodeGetCapabilities(
	ctx context.Context, req *csi.NodeGetCapabilitiesRequest,
) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: d.NSCap,
	}, nil
}

// NodeGetInfo return info of the node on which this plugin is running
func (d *Driver) NodeGetInfo(
	ctx context.Context,
	req *csi.NodeGetInfoRequest,
) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: d.NodeID,
	}, nil
}

// NodeGetVolumeStats get volume stats
func (d *Driver) NodeGetVolumeStats(
	ctx context.Context,
	req *csi.NodeGetVolumeStatsRequest,
) (*csi.NodeGetVolumeStatsResponse, error) {
	if len(req.VolumeId) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"NodeGetVolumeStats volume ID was empty")
	}
	if len(req.VolumePath) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"NodeGetVolumeStats volume path was empty")
	}

	if _, err := os.Lstat(req.VolumePath); err != nil {
		if os.IsNotExist(err) {
			return nil, status.Errorf(codes.NotFound,
				"path %s does not exist", req.VolumePath)
		}
		return nil, status.Errorf(codes.Internal,
			"failed to stat file %s: %v", req.VolumePath, err)
	}

	volumeMetrics, err := volume.NewMetricsStatFS(req.VolumePath).GetMetrics()
	if err != nil {
		return nil, status.Errorf(codes.Internal,
			"failed to get metrics: %v", err)
	}

	available, ok := volumeMetrics.Available.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal,
			"failed to transform volume available size(%v)",
			volumeMetrics.Available)
	}
	capacity, ok := volumeMetrics.Capacity.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal,
			"failed to transform volume capacity size(%v)",
			volumeMetrics.Capacity)
	}
	used, ok := volumeMetrics.Used.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal,
			"failed to transform volume used size(%v)", volumeMetrics.Used)
	}

	inodesFree, ok := volumeMetrics.InodesFree.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal,
			"failed to transform disk inodes free(%v)",
			volumeMetrics.InodesFree)
	}
	inodes, ok := volumeMetrics.Inodes.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal,
			"failed to transform disk inodes(%v)", volumeMetrics.Inodes)
	}
	inodesUsed, ok := volumeMetrics.InodesUsed.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal,
			"failed to transform disk inodes used(%v)",
			volumeMetrics.InodesUsed)
	}

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Unit:      csi.VolumeUsage_BYTES,
				Available: available,
				Total:     capacity,
				Used:      used,
			},
			{
				Unit:      csi.VolumeUsage_INODES,
				Available: inodesFree,
				Total:     inodes,
				Used:      inodesUsed,
			},
		},
	}, nil
}

// ensureMountPoint: create mount point if not exists
// return <true, nil> if it's already a mounted point
// otherwise return <false, nil>
func (d *Driver) ensureMountPoint(target string) (bool, error) {
	notMnt, err := d.mounter.IsLikelyNotMountPoint(target)
	if err != nil && !os.IsNotExist(err) {
		if IsCorruptedDir(target) {
			notMnt = false
			klog.Warningf("detected corrupted mount for targetPath [%s]",
				target)
		} else {
			return !notMnt, err
		}
	}

	if !notMnt {
		// testing original mount point, make sure the mount link is valid
		_, err := ioutil.ReadDir(target)
		if err == nil {
			klog.V(2).Infof("already mounted to target %s", target)
			return !notMnt, nil
		}
		// mount link is invalid, now unmount and remount later
		klog.Warningf("ReadDir %s failed with %v, unmount this directory",
			target, err)
		if err := d.mounter.Unmount(target); err != nil {
			klog.Errorf("Unmount directory %s failed with %v", target, err)
			return !notMnt, err
		}
		notMnt = true
		return !notMnt, err
	}
	if err := volumehelper.MakeDir(target); err != nil {
		klog.Errorf("MakeDir failed on target: %s (%v)", target, err)
		return !notMnt, err
	}
	return !notMnt, nil
}
