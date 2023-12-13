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
	"os"
	"path/filepath"
	"strings"

	volumehelper "sigs.k8s.io/azurelustre-csi-driver/pkg/util"
	"sigs.k8s.io/cloud-provider-azure/pkg/metrics"

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
	_ context.Context,
	req *csi.NodePublishVolumeRequest,
) (*csi.NodePublishVolumeResponse, error) {
	mc := metrics.NewMetricContext(azureLustreCSIDriverName,
		"node_publish_volume",
		"",
		"",
		d.Name)

	volCap := req.GetVolumeCapability()
	if volCap == nil {
		return nil, status.Error(codes.InvalidArgument,
			"Volume capability missing in request")
	}
	userMountFlags := volCap.GetMount().GetMountFlags()

	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
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

	vol, err := newLustreVolume(volumeID, context)
	if err != nil {
		return nil, err
	}

	vol.id = volumeID

	subDirReplaceMap := map[string]string{}

	// get metadata values
	for k, v := range context {
		switch strings.ToLower(k) {
		case podNameKey:
			subDirReplaceMap[podNameMetadata] = v
		case podNamespaceKey:
			subDirReplaceMap[podNamespaceMetadata] = v
		case podUIDKey:
			subDirReplaceMap[podUIDMetadata] = v
		case serviceAccountNameKey:
			subDirReplaceMap[serviceAccountNameMetadata] = v
		case pvcNamespaceKey:
			subDirReplaceMap[pvcNamespaceMetadata] = v
		case pvcNameKey:
			subDirReplaceMap[pvcNameMetadata] = v
		case pvNameKey:
			subDirReplaceMap[pvNameMetadata] = v
		}
	}

	if acquired := d.volumeLocks.TryAcquire(vol.id); !acquired {
		return nil, status.Errorf(codes.Aborted,
			volumeOperationAlreadyExistsFmt,
			vol.id)
	}
	defer d.volumeLocks.Release(vol.id)

	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	source := getSourceString(vol.mgsIPAddress, vol.azureLustreName)

	readOnly := false

	mountOptions := []string{}
	if req.GetReadonly() {
		readOnly = true
		mountOptions = append(mountOptions, "ro")
	}
	for _, userMountFlag := range userMountFlags {
		if userMountFlag == "ro" {
			readOnly = true

			if req.GetReadonly() {
				continue
			}
		}
		mountOptions = append(mountOptions, userMountFlag)
	}

	if len(vol.subDir) > 0 && !d.enableAzureLustreMockMount {
		if readOnly && !vol.retainSubDir {
			return nil, status.Error(
				codes.InvalidArgument,
				"Context retain-sub-dir must be true for a sub-dir on a read-only volume",
			)
		}

		interpolatedSubDir := replaceWithMap(vol.subDir, subDirReplaceMap)

		if isSubpath := ensureStrictSubpath(interpolatedSubDir); !isSubpath {
			return nil, status.Error(
				codes.InvalidArgument,
				"Context sub-dir must be strict subpath",
			)
		}

		if readOnly {
			klog.V(2).Info("NodePublishVolume: not attempting to create sub-dir on read-only volume, assuming existing path")
		} else {
			klog.V(2).Infof(
				"NodePublishVolume: sub-dir will be created at %q",
				interpolatedSubDir,
			)

			if err = d.createSubDir(vol, interpolatedSubDir, mountOptions); err != nil {
				return nil, err
			}
		}

		source = filepath.Join(source, interpolatedSubDir)
		klog.V(2).Infof(
			"NodePublishVolume: full mount source with sub-dir: %q",
			source,
		)
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

	d.kernelModuleLock.Lock()
	err = d.mounter.MountSensitiveWithoutSystemdWithMountFlags(
		source,
		target,
		"lustre",
		mountOptions,
		nil,
		[]string{"--no-mtab"},
	)
	d.kernelModuleLock.Unlock()

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
	isOperationSucceeded = true

	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unmount the volume from the target path
func (d *Driver) NodeUnpublishVolume(
	_ context.Context,
	req *csi.NodeUnpublishVolumeRequest,
) (*csi.NodeUnpublishVolumeResponse, error) {
	mc := metrics.NewMetricContext(azureLustreCSIDriverName,
		"node_unpublish_volume",
		"",
		"",
		d.Name)

	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"Volume ID missing in request")
	}

	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"Target path missing in request")
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

	cleanupSubDir := false

	sourceRoot := ""
	subDirToClean := ""

	var mountOptions []string

	vol, err := getLustreVolFromID(volumeID)
	if err != nil {
		klog.V(2).Infof("failed to parse volume id %q for sub-dir cleanup, skipping", volumeID)
	} else if len(vol.subDir) > 0 && !vol.retainSubDir {
		cleanupSubDir = true
		sourceRoot = getSourceString(vol.mgsIPAddress, vol.azureLustreName)
	}

	if cleanupSubDir {
		foundMountPoint := false
		sourceWithSubDir := ""

		mountPoints, _ := d.mounter.List()
		for _, mountPoint := range mountPoints {
			if mountPoint.Path == targetPath {
				sourceWithSubDir = mountPoint.Device
				foundMountPoint = true

				mountOptions = mountPoint.Opts
				for _, option := range mountOptions {
					if option == "ro" {
						klog.Warning("mounted volume is read only, not attempting to clean up sub-dir")

						cleanupSubDir = false
					}
				}
			}
		}

		switch {
		case !foundMountPoint:
			klog.Warningf("Warning: could not find source for mount point: %q. Skipping sub-dir delete", targetPath)

			cleanupSubDir = false
		case !strings.HasPrefix(sourceWithSubDir, sourceRoot):
			klog.Warningf("Warning: mounted directory %q doesn't appear to be subdirectory of %q. Skipping sub-dir delete",
				sourceWithSubDir,
				sourceRoot,
			)

			cleanupSubDir = false
		case len(strings.TrimPrefix(sourceWithSubDir, sourceRoot)) == 0:
			klog.Warningf("Warning: mounted directory %q isn't a subdirectory of Lustre root. Skipping sub-dir delete",
				sourceWithSubDir,
			)

			cleanupSubDir = false
		default:
			subDirToClean = strings.TrimPrefix(sourceWithSubDir, sourceRoot)
			subDirToClean = strings.Trim(subDirToClean, "/")
		}
	}

	klog.V(2).Infof("NodeUnpublishVolume: unmounting volume %s on %s",
		volumeID, targetPath)
	d.kernelModuleLock.Lock()
	err = mount.CleanupMountPoint(targetPath, d.mounter,
		true /*extensiveMountPointCheck*/)
	d.kernelModuleLock.Unlock()
	if err != nil {
		return nil, status.Errorf(codes.Internal,
			"failed to unmount target %q: %v", targetPath, err)
	}
	klog.V(2).Infof(
		"NodeUnpublishVolume: unmount volume %s on %s successfully",
		volumeID,
		targetPath,
	)

	if cleanupSubDir {
		klog.V(2).Infof(
			"NodeUnpublishVolume: deleting subdirectory %q within %q",
			subDirToClean,
			sourceRoot,
		)

		if err = d.deleteSubDir(vol, subDirToClean, mountOptions); err != nil {
			return nil, err
		}
	} else {
		klog.V(2).Info("Not attempting to clean sub-dir")
	}

	isOperationSucceeded = true

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// Staging and Unstaging is not able to be supported with how Lustre is mounted
//
// This was discovered during a proof of concept implementation and the issue
// is as follows:
//
// When the kubelet process attempts to unstage / unmount a Lustre mount that
// has been staged to a global mount point, it performs extra checks to ensure
// that the same device is not mounted anywhere else in the filesystem. For
// usual configurations, this would be a reasonable check to ensure that we
// aren't trying to remove something that is still in use elsewhere in the
// system. However, the way Lustre mounts are configured is not compatible
// with the check it performs.
//
// The kubelet process does this by checking all of the mount points on the
// node to see if any have the following:
// 1) The same 'root' value of the mount that is being cleaned
// 2) The same device number of the mount that is being cleaned
// And that those mounts are in a different path tree.
// If so, it returns this error: "the device mount path %q is still mounted
// by other references %v", deviceMountPath, refs) and fails the unmount.
// See pkg/volume/util/operationexecutor/operation_generator.go
// calling GetDeviceMountRefs(deviceMountPath) around line 947.
//
// All Lustre mounts on a system, no matter where in the lustrefs they are
// mounted to, all have '/' as the root and they all have the same major and
// minor device numbers, so as far as this check is concerned, every lustre
// mount is the same device, even though individual Lustre mount points can
// be unmounted without affecting others and should not be a concern.
//
// With a single Lustre volume mount, this works fine. It stages to a
// globalpath dir, pods can bind mount into that, and when the last pod is
// done, unstage is called and the global mount point can be cleaned up,
// because that is the only lustre mount so kubelet has no issue with
// 'other mounts' on the same node.
//
// The problem occurs when two different volumes are trying to mount a
// Lustre cluster. In that case, pods for the first volume can come up
// as expected with their global mount path, then pods for the second
// volume with their global mount path. The error occurs when the pods
// for one of these volumes are deleted and an unstage action should occur,
// because the other volume has its own Lustre mount, so it fails this
// check. For example, it's trying to unmount
// /var/...<firstvolume>.../globalpath, but there's another volume at
// /var/...<secondvolume>.../globalpath with the same root '/' and major
// and minor device numbers.
//
// It errors out, fails the unmount, and never calls unstage, even
// though all of the pods using that volume have already been deleted.
// This leaves the box with as many global mount directories still mounted
// to the Lustre cluster as you've ever staged, but without any way to see
// this other than looking at the mounts on the node or in the kubelet logs.
func (d *Driver) NodeStageVolume(_ context.Context, _ *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// Staging and Unstaging is not able to be supported with how Lustre is mounted
//
// See NodeStageVolume for more details
func (d *Driver) NodeUnstageVolume(_ context.Context, _ *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// NodeGetCapabilities return the capabilities of the Node plugin
func (d *Driver) NodeGetCapabilities(
	_ context.Context, _ *csi.NodeGetCapabilitiesRequest,
) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: d.NSCap,
	}, nil
}

// NodeGetInfo return info of the node on which this plugin is running
func (d *Driver) NodeGetInfo(
	_ context.Context,
	_ *csi.NodeGetInfoRequest,
) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: d.NodeID,
	}, nil
}

// NodeGetVolumeStats get volume stats
func (d *Driver) NodeGetVolumeStats(
	_ context.Context,
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
		_, err := os.ReadDir(target)
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

func (d *Driver) createSubDir(vol *lustreVolume, subDirPath string, mountOptions []string) error {
	if err := d.internalMount(vol, mountOptions); err != nil {
		return err
	}

	defer func() {
		if err := d.internalUnmount(vol); err != nil {
			klog.Warningf("failed to unmount lustre server: %v", err.Error())
		}
	}()

	internalVolumePath, err := getInternalVolumePath(d.workingMountDir, vol, subDirPath)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Making subdirectory at %q", internalVolumePath)

	if err := os.MkdirAll(internalVolumePath, 0775); err != nil {
		return status.Errorf(codes.Internal, "failed to make subdirectory: %v", err.Error())
	}

	return nil
}

func (d *Driver) deleteSubDir(vol *lustreVolume, subDirPath string, mountOptions []string) error {
	if err := d.internalMount(vol, mountOptions); err != nil {
		return err
	}

	defer func() {
		if err := d.internalUnmount(vol); err != nil {
			klog.Warningf("failed to unmount lustre server: %v", err.Error())
		}
	}()

	internalVolumePath, err := getInternalVolumePath(d.workingMountDir, vol, subDirPath)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Removing subdirectory at %q", internalVolumePath)

	if err = os.RemoveAll(internalVolumePath); err != nil {
		return status.Errorf(codes.Internal, "failed to delete subdirectory: %v", err.Error())
	}

	return nil
}

func getSourceString(mgsIPAddress, azureLustreName string) string {
	return fmt.Sprintf("%s@tcp:/%s", mgsIPAddress, azureLustreName)
}

func getInternalMountPath(workingMountDir string, vol *lustreVolume) (string, error) {
	if vol == nil || len(vol.id) == 0 {
		return "", status.Error(codes.Internal, "cannot get internal mount path for nil or empty volume")
	}

	if isSubpath := ensureStrictSubpath(vol.id); !isSubpath {
		return "", status.Errorf(
			codes.InvalidArgument,
			"volume name or id %q must be interpretable as a strict subpath",
			vol.id,
		)
	}

	return filepath.Join(workingMountDir, vol.id), nil
}

func getInternalVolumePath(workingMountDir string, vol *lustreVolume, subDirPath string) (string, error) {
	internalMountPath, err := getInternalMountPath(workingMountDir, vol)
	if err != nil {
		return "", err
	}

	if isSubpath := ensureStrictSubpath(subDirPath); !isSubpath {
		return "", status.Errorf(
			codes.InvalidArgument,
			"sub-dir %q must be strict subpath",
			subDirPath,
		)
	}

	return filepath.Join(internalMountPath, subDirPath), nil
}

func (d *Driver) internalMount(vol *lustreVolume, mountOptions []string) error {
	source := getSourceString(vol.mgsIPAddress, vol.azureLustreName)

	target, err := getInternalMountPath(d.workingMountDir, vol)
	if err != nil {
		return err
	}

	klog.V(4).Infof("internally mounting %v", target)

	mnt, err := d.ensureMountPoint(target)
	if err != nil {
		return status.Errorf(codes.Internal,
			"Could not mount target %q: %v",
			target,
			err)
	}

	if mnt {
		klog.Warningf(
			"volume %q is already mounted on %q",
			vol.id,
			target,
		)

		err = d.internalUnmount(vol)
		if err != nil {
			return status.Errorf(codes.Internal,
				"Could not unmount existing volume at %q: %v",
				target,
				err)
		}
	}

	klog.V(2).Infof(
		"volume %q mounting %q at %q with mountOptions: %v",
		vol.id, source, target, mountOptions,
	)

	d.kernelModuleLock.Lock()
	err = d.mounter.MountSensitiveWithoutSystemdWithMountFlags(
		source,
		target,
		"lustre",
		mountOptions,
		nil,
		[]string{"--no-mtab"},
	)
	d.kernelModuleLock.Unlock()

	if err != nil {
		if removeErr := os.Remove(target); removeErr != nil {
			return status.Errorf(
				codes.Internal,
				"Could not remove mount target %q: %v",
				target,
				removeErr,
			)
		}

		return status.Errorf(codes.Internal,
			"Could not mount %q at %q: %v", source, target, err)
	}

	return nil
}

func (d *Driver) internalUnmount(vol *lustreVolume) error {
	target, err := getInternalMountPath(d.workingMountDir, vol)
	if err != nil {
		return err
	}

	klog.V(4).Infof("internally unmounting %v", target)

	err = mount.CleanupMountPoint(target, d.mounter, true)
	if err != nil {
		err = status.Errorf(codes.Internal, "failed to unmount staging target %q: %v", target, err)
	}

	return err
}

// Ensures that the given subpath, when joined with any base path, will be a path
// within the given base path, and not equal to it. This ensures that the this
// subpath value can be safely created or deleted under the base path without
// affecting other data in the base path.
func ensureStrictSubpath(subPath string) bool {
	return filepath.IsLocal(subPath) && filepath.Clean(subPath) != "."
}

type lustreVolume struct {
	name            string
	id              string
	mgsIPAddress    string
	azureLustreName string
	subDir          string
	retainSubDir    bool
}

func getLustreVolFromID(id string) (*lustreVolume, error) {
	segments := strings.Split(id, separator)
	if len(segments) < 3 {
		return nil, fmt.Errorf("could not split volume id %q into lustre name and ip address", id)
	}

	name := segments[0]
	vol := &lustreVolume{
		name:            name,
		id:              id,
		azureLustreName: strings.Trim(segments[1], "/"),
		mgsIPAddress:    segments[2],
	}

	if len(segments) >= 4 {
		vol.subDir = strings.Trim(segments[3], "/")

		retainSubDirString := strings.ToLower(segments[4])
		if len(retainSubDirString) == 0 {
			vol.retainSubDir = true
		} else {
			if retainSubDirString != "true" && retainSubDirString != "false" {
				return nil, fmt.Errorf("could not parse retain-sub-dir value %q into boolean", retainSubDirString)
			}
			vol.retainSubDir = retainSubDirString == "true"
		}
	} else {
		vol.retainSubDir = true
	}

	return vol, nil
}

// Convert context parameters to a lustreVolume
func newLustreVolume(volumeID string, params map[string]string) (*lustreVolume, error) {
	var mgsIPAddress, azureLustreName, subDir string
	// Shouldn't attempt to delete anything unless sub-dir is actually specified
	retainSubDir := true
	subDirReplaceMap := map[string]string{}
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
				return nil, status.Error(
					codes.InvalidArgument,
					"Context sub-dir must not be empty or root if provided",
				)
			}

			if _, ok := params[VolumeContextRetainSubDir]; !ok {
				return nil, status.Error(
					codes.InvalidArgument,
					"Context retain-sub-dir must be provided when sub-dir is provided",
				)
			}
		case VolumeContextRetainSubDir:
			retainSubDirString := strings.ToLower(v)
			if retainSubDirString != "true" && retainSubDirString != "false" {
				return nil, status.Error(
					codes.InvalidArgument,
					"Context retain-sub-dir value must be either true or false",
				)
			}

			retainSubDir = retainSubDirString == "true"

			if _, ok := params[VolumeContextSubDir]; !ok {
				return nil, status.Error(
					codes.InvalidArgument,
					"Context sub-dir must be provided when retain-sub-dir is provided",
				)
			}
		case podNameKey:
			subDirReplaceMap[podNameMetadata] = v
		case podNamespaceKey:
			subDirReplaceMap[podNamespaceMetadata] = v
		case podUIDKey:
			subDirReplaceMap[podUIDMetadata] = v
		case serviceAccountNameKey:
			subDirReplaceMap[serviceAccountNameMetadata] = v
		case pvcNamespaceKey:
			subDirReplaceMap[pvcNamespaceMetadata] = v
		case pvcNameKey:
			subDirReplaceMap[pvcNameMetadata] = v
		case pvNameKey:
			subDirReplaceMap[pvNameMetadata] = v
		}
	}

	if len(mgsIPAddress) == 0 {
		return nil, status.Error(
			codes.InvalidArgument,
			"Context mgs-ip-address must be provided",
		)
	}

	azureLustreName = strings.Trim(azureLustreName, "/")
	if len(azureLustreName) == 0 {
		return nil, status.Error(
			codes.InvalidArgument,
			"Context fs-name must be provided",
		)
	}

	volName := ""

	volFromID, err := getLustreVolFromID(volumeID)
	if err != nil {
		klog.Warningf("error parsing volume id '%v'", err)
	} else {
		volName = volFromID.name
	}

	vol := &lustreVolume{
		name:            volName,
		mgsIPAddress:    mgsIPAddress,
		azureLustreName: azureLustreName,
		subDir:          subDir,
		retainSubDir:    retainSubDir,
		id: fmt.Sprintf(
			volumeIDTemplate,
			volName,
			azureLustreName,
			mgsIPAddress,
			subDir,
			retainSubDir),
	}

	if volFromID != nil && *volFromID != *vol {
		klog.Warningf("volume context does not match values in volume id for volume %q", volumeID)
	}

	return vol, nil
}
