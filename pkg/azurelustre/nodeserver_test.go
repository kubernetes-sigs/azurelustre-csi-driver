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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mount "k8s.io/mount-utils"
	testingexec "k8s.io/utils/exec/testing"
)

const (
	targetTest = "target_test"
	subDir     = "testSubDir"
)

func TestNodeGetInfo(t *testing.T) {
	d := NewFakeDriver(t)

	// Test valid request
	req := csi.NodeGetInfoRequest{}
	resp, err := d.NodeGetInfo(t.Context(), &req)
	require.NoError(t, err)
	assert.Equal(t, fakeNodeID, resp.GetNodeId())
}

func TestNodeGetCapabilities(t *testing.T) {
	d := NewFakeDriver(t)
	capType := &csi.NodeServiceCapability_Rpc{
		Rpc: &csi.NodeServiceCapability_RPC{
			Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		},
	}
	capList := []*csi.NodeServiceCapability{{
		Type: capType,
	}}
	d.NSCap = capList
	// Test valid request
	req := csi.NodeGetCapabilitiesRequest{}
	resp, err := d.NodeGetCapabilities(t.Context(), &req)
	assert.NotNil(t, resp)
	assert.Equal(t, capType, resp.GetCapabilities()[0].GetType())
	require.NoError(t, err)
}

func TestEnsureMountPoint(t *testing.T) {
	lookupFailure := errors.New("mount lookup failed")
	tests := []struct {
		name            string
		onDisk          string
		notMountPoint   bool
		lookupError     error
		expectedError   error
		mayBeMounted    bool
		expectedUnmount bool
	}{
		{name: "lookup failure leaves mount state uncertain", onDisk: "absent", lookupError: lookupFailure, expectedError: lookupFailure, mayBeMounted: true},
		{name: "unreadable reported mount is unmounted and target recreated", onDisk: "absent", notMountPoint: false, expectedUnmount: true},
		{name: "regular file cannot be used as a target directory", onDisk: "file", notMountPoint: true, expectedError: syscall.ENOTDIR},
		{name: "unmounted missing directory is created", onDisk: "absent", notMountPoint: true},
		{name: "readable existing mount is reused", onDisk: "directory", notMountPoint: false, mayBeMounted: true},
		{name: "lookup NotExist creates an unmounted directory", onDisk: "absent", lookupError: os.ErrNotExist},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "target")
			switch testCase.onDisk {
			case "directory":
				require.NoError(t, os.Mkdir(target, 0o700))
			case "file":
				require.NoError(t, os.WriteFile(target, []byte("not a directory"), 0o600))
			case "absent":
			default:
				t.Fatalf("unknown target fixture %q", testCase.onDisk)
			}
			driver := NewFakeDriver(t)
			mounter := &fakeMounter{notMountPoint: new(testCase.notMountPoint), mountCheckError: testCase.lookupError}
			driver.mounter = &mount.SafeFormatAndMount{Interface: mounter}

			mounted, err := driver.ensureMountPoint(target)

			assert.Equal(t, testCase.mayBeMounted, mounted, "reported mount certainty must match the observed state")
			if testCase.expectedError != nil {
				require.ErrorIs(t, err, testCase.expectedError)
			} else {
				require.NoError(t, err)
				assert.DirExists(t, target)
			}
			if testCase.expectedUnmount {
				assert.Equal(t, []mount.FakeAction{{Action: "unmount", Target: target}}, mounter.GetLog())
			} else {
				assert.Empty(t, mounter.GetLog())
			}
		})
	}
}

func TestCreateSubDirValidation(t *testing.T) {
	testCases := []struct {
		name            string
		mountPath       string
		subDirPath      string
		mountCheckError error
		expectedCode    codes.Code
	}{
		{name: "invalid mount path", mountPath: "../target", subDirPath: "child", expectedCode: codes.Internal},
		{name: "invalid subdirectory", mountPath: "target", subDirPath: "../child", expectedCode: codes.InvalidArgument},
		{name: "failed mount preparation", mountPath: "target", subDirPath: "child", mountCheckError: errors.New("lookup failed"), expectedCode: codes.Unavailable},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			driver, mounter, _ := newNodeMountTestDriver(t)
			mounter.mountCheckError = test.mountCheckError

			err := driver.createSubDir(&lustreVolume{mgsIPAddress: "127.0.0.1", azureLustreName: DefaultLustreFsName}, test.mountPath, test.subDirPath, nil)

			require.Error(t, err)
			assert.Equal(t, test.expectedCode, status.Code(err))
			assert.Empty(t, mounter.GetLog(), "invalid paths and failed preparation must not mount")
		})
	}
}

func TestInternalMountAndCleanupRejectEscapingPaths(t *testing.T) {
	t.Run("mount rejects traversal before filesystem work", func(t *testing.T) {
		driver, mounter, _ := newNodeMountTestDriver(t)

		err := driver.internalMount(&lustreVolume{mgsIPAddress: "127.0.0.1", azureLustreName: DefaultLustreFsName}, "../target", nil)

		require.Error(t, err)
		assert.Empty(t, mounter.GetLog())
	})
	t.Run("cleanup rejects traversal before unmount", func(t *testing.T) {
		driver, mounter, _ := newNodeMountTestDriver(t)

		err := driver.cleanupInternalMountIfPresent("../target")

		require.Error(t, err)
		assert.Empty(t, mounter.GetLog())
	})
}

func TestNodeUnpublishRetriesTemporaryCleanup(t *testing.T) {
	driver, mounter, backingDirectory := newNodeMountTestDriver(t)
	mounter.unmountError = errors.New("temporary unmount failed")
	target := filepath.Join(t.TempDir(), "target")
	request := &csi.NodePublishVolumeRequest{
		VolumeId:   "volume#lustrefs#127.0.0.1#child#f#",
		TargetPath: target,
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
		},
		VolumeContext: map[string]string{VolumeContextMGSIPAddress: "127.0.0.1", VolumeContextSubDir: "child"},
	}

	response, err := driver.NodePublishVolume(t.Context(), request)

	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Nil(t, response)
	require.ErrorContains(t, err, "temporary unmount failed")
	require.Len(t, mounter.MountPoints, 1, "only the temporary mount may remain")
	temporaryMount := mounter.MountPoints[0]
	assert.NotEqual(t, target, temporaryMount.Path, "failed preparation must not leave a published target")
	unpublish := &csi.NodeUnpublishVolumeRequest{VolumeId: request.GetVolumeId(), TargetPath: target}

	_, err = driver.NodeUnpublishVolume(t.Context(), unpublish)

	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Equal(t, []mount.MountPoint{temporaryMount}, mounter.MountPoints, "failed cleanup must keep the same temporary mount visible")
	mounter.unmountError = nil

	_, err = driver.NodeUnpublishVolume(t.Context(), unpublish)

	require.NoError(t, err)
	assert.Empty(t, mounter.MountPoints)
	assert.NoDirExists(t, target)
	assert.DirExists(t, filepath.Join(backingDirectory, "0", "child"), "the first unmounted snapshot must retain the simulated filesystem data")

	_, err = driver.NodeUnpublishVolume(t.Context(), unpublish)

	require.NoError(t, err, "cleanup must remain idempotent")
}

func TestNodePublishRetriesTemporaryCleanupBeforeProbe(t *testing.T) {
	driver, mounter, backingDirectory := newNodeMountTestDriver(t)
	mounter.unmountError = errors.New("temporary unmount failed")
	target := filepath.Join(t.TempDir(), "target")
	request := &csi.NodePublishVolumeRequest{
		VolumeId:   "volume#lustrefs#127.0.0.1#child#f#",
		TargetPath: target,
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
		},
		VolumeContext: map[string]string{VolumeContextMGSIPAddress: "127.0.0.1", VolumeContextSubDir: "child"},
	}

	_, err := driver.NodePublishVolume(t.Context(), request)

	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Len(t, mounter.MountPoints, 1)
	probeCalls := 0
	driver.pingChecker = &fakePingChecker{
		err: status.Error(codes.FailedPrecondition, "probe failed"),
		onCheck: func(address string) {
			probeCalls++
			assert.Equal(t, "127.0.0.1", address)
		},
	}

	_, err = driver.NodePublishVolume(t.Context(), request)

	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err), "a failed probe must not discard unfinished temporary cleanup")
	require.ErrorContains(t, err, "temporary unmount failed")
	assert.Len(t, mounter.MountPoints, 1)
	assert.Zero(t, probeCalls, "a pending cleanup must stop publish before the probe is called")
	mounter.unmountError = nil

	_, err = driver.NodePublishVolume(t.Context(), request)

	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "the probe can report its own error once temporary cleanup is complete")
	require.ErrorContains(t, err, "probe failed")
	assert.Equal(t, 1, probeCalls, "only the retry after successful cleanup may run the probe")
	assert.Empty(t, mounter.MountPoints)
	assert.DirExists(t, filepath.Join(backingDirectory, "0", "child"), "the first unmount snapshot must retain the simulated filesystem data")
}

func TestCreateSubDirPreservesCleanupErrors(t *testing.T) {
	driver, mounter, _ := newNodeMountTestDriver(t)
	mounter.unmountError = errors.New("temporary unmount failed")
	internalTarget := filepath.Join(driver.workingMountDir, "target")
	err := os.Mkdir(internalTarget, 0o700)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(internalTarget, "occupied"), []byte("test"), 0o600)
	require.NoError(t, err)

	err = driver.createSubDir(&lustreVolume{mgsIPAddress: "127.0.0.1", azureLustreName: DefaultLustreFsName}, "target", "occupied/child", nil)

	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	require.ErrorContains(t, err, "failed to make subdirectory")
	require.ErrorContains(t, err, "temporary unmount failed")
	assert.Len(t, mounter.MountPoints, 1)
}

func TestPublishAndInternalMountPreservePreparationUncertainty(t *testing.T) {
	testCases := []struct {
		name            string
		mountCheckError error
		unmountError    error
	}{
		{name: "mount lookup cannot establish current state", mountCheckError: errors.New("mount lookup failed")},
		{name: "mount table claims a mount but its unreadable target cannot be unmounted", unmountError: errors.New("unmount failed")},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			driver := NewFakeDriver(t)
			driver.workingMountDir = t.TempDir()
			target := filepath.Join(driver.workingMountDir, "mounted-target")
			fakeMount := &fakeMounter{notMountPoint: new(false), mountCheckError: test.mountCheckError, unmountError: test.unmountError}
			driver.mounter = &mount.SafeFormatAndMount{Interface: fakeMount}
			forceMounter := mount.MounterForceUnmounter(fakeMount)
			driver.forceMounter = &forceMounter
			mountPoint := mount.MountPoint{Device: "127.0.0.1@tcp:/lustrefs", Path: target, Type: "lustre"}
			fakeMount.MountPoints = []mount.MountPoint{mountPoint}
			request := &csi.NodePublishVolumeRequest{
				VolumeId:         "volume#lustrefs#127.0.0.1##f#",
				TargetPath:       target,
				VolumeCapability: &csi.VolumeCapability{},
				VolumeContext:    map[string]string{VolumeContextMGSIPAddress: "127.0.0.1"},
			}

			response, err := driver.NodePublishVolume(t.Context(), request)

			require.Error(t, err)
			assert.Equal(t, codes.Unavailable, status.Code(err), "kubelet must retain uncertain mount state")
			assert.Nil(t, response)
			assert.Equal(t, []mount.MountPoint{mountPoint}, fakeMount.MountPoints)
			acquired := driver.volumeLocks.TryAcquire(request.GetVolumeId() + "-" + target)
			require.True(t, acquired, "the failed publish must release its operation lock")
			driver.volumeLocks.Release(request.GetVolumeId() + "-" + target)

			err = driver.internalMount(&lustreVolume{mgsIPAddress: "127.0.0.1", azureLustreName: DefaultLustreFsName}, filepath.Base(target), nil)

			require.Error(t, err)
			assert.Equal(t, codes.Unavailable, status.Code(err), "temporary mount uncertainty must survive propagation")
			assert.Equal(t, []mount.MountPoint{mountPoint}, fakeMount.MountPoints)
		})
	}
}

func TestFailedMountError(t *testing.T) {
	testCases := []struct {
		name         string
		createTarget bool
		createEntry  bool
		expectedCode codes.Code
	}{
		{name: "empty target can be removed after mount failure", createTarget: true, expectedCode: codes.Internal},
		{name: "already absent target leaves no uncertain mount", expectedCode: codes.Internal},
		{name: "nonempty target cannot be removed safely", createTarget: true, createEntry: true, expectedCode: codes.Unavailable},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "target")
			if test.createTarget {
				err := os.Mkdir(target, 0o700)
				require.NoError(t, err)
			}
			if test.createEntry {
				err := os.WriteFile(filepath.Join(target, "entry"), []byte("test"), 0o600)
				require.NoError(t, err)
			}

			err := failedMountError("127.0.0.1@tcp:/lustrefs", target, errors.New("mount failed"))

			require.Error(t, err)
			assert.Equal(t, test.expectedCode, status.Code(err), "cleanup certainty must determine the returned status; got %v", err)
			require.ErrorContains(t, err, "mount failed")
			if test.createEntry {
				require.ErrorContains(t, err, "could not remove mount target")
				assert.FileExists(t, filepath.Join(target, "entry"))
			} else {
				assert.NoDirExists(t, target)
			}
		})
	}
}

func TestInternalMountReportsMountFailureAfterRemovingEmptyTarget(t *testing.T) {
	driver := NewFakeDriver(t)
	driver.workingMountDir = t.TempDir()
	driver.mounter = &mount.SafeFormatAndMount{Interface: &fakeMounter{mountError: errors.New("injected mount failure")}}
	mountPath := "target"

	err := driver.internalMount(&lustreVolume{mgsIPAddress: "127.0.0.1", azureLustreName: DefaultLustreFsName}, mountPath, nil)

	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err), "successful cleanup establishes a final failed mount")
	require.ErrorContains(t, err, "injected mount failure")
	assert.NoDirExists(t, filepath.Join(driver.workingMountDir, mountPath))
}

func TestInternalMountRecreatesExistingTarget(t *testing.T) {
	driver, mounter, backingDirectory := newNodeMountTestDriver(t)
	mounter.notMountPoint = new(false)
	mountPath := "existing-target"
	target := filepath.Join(driver.workingMountDir, mountPath)
	err := os.Mkdir(target, 0o700)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(target, "entry"), []byte("test"), 0o600)
	require.NoError(t, err)
	err = mounter.Mount("127.0.0.1@tcp:/lustrefs", target, "lustre", nil)
	require.NoError(t, err)

	err = driver.internalMount(&lustreVolume{mgsIPAddress: "127.0.0.1", azureLustreName: DefaultLustreFsName}, mountPath, nil)

	require.NoError(t, err)
	assert.DirExists(t, target)
	assert.Len(t, mounter.MountPoints, 1, "preparation must replace the old temporary mount")
	assert.FileExists(t, filepath.Join(backingDirectory, "0", "entry"), "remount preparation must preserve old filesystem data")
}

func TestInternalMountPreservesExistingMountWhenUnmountFails(t *testing.T) {
	driver := NewFakeDriver(t)
	driver.workingMountDir = t.TempDir()
	target := filepath.Join(driver.workingMountDir, "mounted-target")
	err := os.Mkdir(target, 0o700)
	require.NoError(t, err)
	fakeMount := &fakeMounter{notMountPoint: new(false), unmountError: errors.New("existing unmount failed")}
	fakeMount.MountPoints = []mount.MountPoint{{Device: "127.0.0.1@tcp:/lustrefs", Path: target, Type: "lustre"}}
	driver.mounter = &mount.SafeFormatAndMount{Interface: fakeMount}
	forceMounter := mount.MounterForceUnmounter(fakeMount)
	driver.forceMounter = &forceMounter

	err = driver.internalMount(&lustreVolume{mgsIPAddress: "127.0.0.1", azureLustreName: DefaultLustreFsName}, filepath.Base(target), nil)

	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	require.ErrorContains(t, err, "existing unmount failed")
	assert.Len(t, fakeMount.MountPoints, 1)
}

func TestNodePublishVolume(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Errorf("failed to get current working directory")
	}

	workingMountDir := filepath.Join(workingDirectory, "workingMountDir")

	volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}
	alreadyExistTarget := "./false_is_likely_exist_target"
	metadataVolumeID := "vol_1#lustrefs#1.1.1.1#testNestedSubDir/${pod.metadata.name}/${pod.metadata.namespace}/${pod.metadata.uid}/${serviceAccount.metadata.name}/${pvc.metadata.name}/${pvc.metadata.namespace}/${pv.metadata.name}/testNestedSubDir"
	createDirError := status.Errorf(codes.Internal,
		"Could not mount target \"./azurelustre.go\": mkdir ./azurelustre.go: not a directory")
	lockKey := fmt.Sprintf("%s-%s", "vol_1#lustrefs#1.1.1.1#testSubDir", targetTest)
	tests := []struct {
		desc                 string
		setup                func(*Driver)
		req                  csi.NodePublishVolumeRequest
		expectedErr          error
		expectedMountpoints  []mount.MountPoint
		expectedMountActions []mount.FakeAction
		cleanup              func(*Driver)
	}{
		{
			desc:                 "Volume capabilities missing",
			req:                  csi.NodePublishVolumeRequest{},
			expectedErr:          status.Error(codes.InvalidArgument, "Volume capability missing in request"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc:                 "Volume ID missing",
			req:                  csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap}},
			expectedErr:          status.Error(codes.InvalidArgument, "Volume ID missing in request"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Volume context missing",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1##",
				TargetPath:       targetTest,
			},
			expectedErr:          status.Error(codes.InvalidArgument, "Volume context must be provided"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Target path missing",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#",
				VolumeContext:    map[string]string{"fs-name": "lustrefs"},
			},
			expectedErr:          status.Error(codes.InvalidArgument, "Target path not provided"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "MGS IP address missing",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"fs-name": "lustrefs"},
			},
			expectedErr:          status.Error(codes.InvalidArgument, "Context mgs-ip-address must be provided"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Valid request without lustre fs name",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1##1.1.1.1",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1"},
			},
			expectedErr:          nil,
			expectedMountpoints:  []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"}},
		},
		{
			desc: "Valid request with old ID",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
			},
			expectedErr:          nil,
			expectedMountpoints:  []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"}},
		},
		{
			desc: "Empty sub-dir",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": ""},
				Readonly:         false,
			},
			expectedErr:          status.Error(codes.InvalidArgument, "Context sub-dir must not be empty or root if provided"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Invalid root sub-dir",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#/",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": "/"},
				Readonly:         false,
			},
			expectedErr:          status.Error(codes.InvalidArgument, "Context sub-dir must not be empty or root if provided"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Invalid sub-dir to parent",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#../../parentAttemptSubDir",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": "../../parentAttemptSubDir"},
				Readonly:         false,
			},
			expectedErr:          status.Error(codes.InvalidArgument, "Context sub-dir must be strict subpath"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Valid request read only",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir},
				Readonly:      true,
			},
			expectedErr:          nil,
			expectedMountpoints:  []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs/testSubDir", Path: "target_test", Type: "lustre", Opts: []string{"ro", "noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs/testSubDir", FSType: "lustre"}},
		},
		{
			desc: "Valid mount options, no sub-dir",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
				Readonly:      false,
			},
			expectedErr:          nil,
			expectedMountpoints:  []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"}},
		},
		{
			desc: "Valid mount options with dynamic provisioning",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1##t#test-amlfilesystem-rg",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "amlfilesystem-name": "test-amlfilesystem-name", "resource-group-name": "test-amlfilesystem-rg", "created-by-dynamic-provisioning": "t"},
				Readonly:      false,
			},
			expectedErr:          nil,
			expectedMountpoints:  []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"}},
		},
		{
			desc: "Valid mount options with sub-dir",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir},
				Readonly:      false,
			},
			expectedErr:         nil,
			expectedMountpoints: []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs/testSubDir", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{
				{Action: "mount", Target: workingMountDir + "/target_test", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"},
				{Action: "unmount", Target: workingMountDir + "/target_test", Source: "", FSType: ""},
				{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs/testSubDir", FSType: "lustre"},
			},
		},
		{
			desc: "Unexpected volume ID doesn't skip sub-dir creation",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir},
				Readonly:      false,
			},
			expectedErr:         nil,
			expectedMountpoints: []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs/testSubDir", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{
				{Action: "mount", Target: workingMountDir + "/target_test", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"},
				{Action: "unmount", Target: workingMountDir + "/target_test", Source: "", FSType: ""},
				{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs/testSubDir", FSType: "lustre"},
			},
		},
		{
			desc: "Valid mount options with slashes in paths",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs/", "sub-dir": "/testSubDir/"},
				Readonly:      false,
			},
			expectedErr:         nil,
			expectedMountpoints: []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs/testSubDir", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{
				{Action: "mount", Target: workingMountDir + "/target_test", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"},
				{Action: "unmount", Target: workingMountDir + "/target_test", Source: "", FSType: ""},
				{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs/testSubDir", FSType: "lustre"},
			},
		},
		{
			desc: "Valid mount options with metadata",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:   metadataVolumeID,
				TargetPath: targetTest,
				VolumeContext: map[string]string{
					"mgs-ip-address":                         "1.1.1.1",
					"fs-name":                                "lustrefs",
					"sub-dir":                                "testNestedSubDir/${pod.metadata.name}/${pod.metadata.namespace}/${pod.metadata.uid}/${serviceAccount.metadata.name}/${pvc.metadata.name}/${pvc.metadata.namespace}/${pv.metadata.name}/testNestedSubDir",
					"csi.storage.k8s.io/pod.name":            "testPodName",
					"csi.storage.k8s.io/pod.namespace":       "testPodNamespace",
					"csi.storage.k8s.io/pod.uid":             "testPodUid",
					"csi.storage.k8s.io/serviceAccount.name": "testServiceAccountName",
					"csi.storage.k8s.io/pvc/name":            "testPvcName",
					"csi.storage.k8s.io/pvc/namespace":       "testPvcNamespace",
					"csi.storage.k8s.io/pv/name":             "testPvName",
				},
				Readonly: false,
			},
			expectedErr: nil,
			expectedMountpoints: []mount.MountPoint{
				{
					Device: "1.1.1.1@tcp:/lustrefs/testNestedSubDir/testPodName/testPodNamespace/testPodUid/testServiceAccountName/testPvcName/testPvcNamespace/testPvName/testNestedSubDir",
					Path:   "target_test",
					Type:   "lustre",
					Opts:   []string{"noatime", "flock"},
				},
			},
			expectedMountActions: []mount.FakeAction{
				{Action: "mount", Target: filepath.Join(workingMountDir, targetTest), Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"},
				{Action: "unmount", Target: filepath.Join(workingMountDir, targetTest), Source: "", FSType: ""},
				{
					Action: "mount",
					Target: "target_test",
					Source: "1.1.1.1@tcp:/lustrefs/testNestedSubDir/testPodName/testPodNamespace/testPodUid/testServiceAccountName/testPvcName/testPvcNamespace/testPvName/testNestedSubDir",
					FSType: "lustre",
				},
			},
		},
		{
			desc: "Valid mount options with duplicated readonly",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock", "ro"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir},
				Readonly:      true,
			},
			expectedErr:          nil,
			expectedMountpoints:  []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs/testSubDir", Path: "target_test", Type: "lustre", Opts: []string{"ro", "noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs/testSubDir", FSType: "lustre"}},
		},
		{
			desc: "Error creating directory",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#",
				TargetPath:       "./azurelustre.go",
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
				Readonly:         true,
			},
			expectedErr:          createDirError,
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Internal mount path already mounted",
			setup: func(d *Driver) {
				d.workingMountDir = workingMountDir
				err = makeDir(filepath.Join(workingMountDir, targetTest))
				require.NoError(t, err)
				err = d.mounter.Mount("1.1.1.1@tcp:/lustrefs/existing", filepath.Join(workingMountDir, targetTest), "lustre", []string{"noatime", "flock"})
				require.NoError(t, err)
			},
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir},
				Readonly:      false,
			},
			expectedErr:         nil,
			expectedMountpoints: []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs/testSubDir", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{
				{Action: "unmount", Target: filepath.Join(workingMountDir, targetTest), Source: "", FSType: ""},
				{Action: "mount", Target: filepath.Join(workingMountDir, targetTest), Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"},
				{Action: "unmount", Target: filepath.Join(workingMountDir, targetTest), Source: "", FSType: ""},
				{Action: "mount", Target: targetTest, Source: "1.1.1.1@tcp:/lustrefs/testSubDir", FSType: "lustre"},
			},
		},
		{
			desc: "Success already mounted",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#testSubDir",
				TargetPath:       alreadyExistTarget,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir},
				Readonly:         true,
			},
			expectedErr:          nil,
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Success already mounted returns OK even when cluster ping is unreachable",
			setup: func(d *Driver) {
				d.pingChecker = &fakePingChecker{err: status.Error(codes.FailedPrecondition, "MGS IP address is not reachable")}
			},
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#testSubDir",
				TargetPath:       alreadyExistTarget,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir},
				Readonly:         true,
			},
			expectedErr:          nil,
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Error could not mount",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#",
				TargetPath:       "error_mount_sens_mountflags",
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
				Readonly:         true,
			},
			expectedErr: status.Error(codes.Internal,
				"Could not mount \"1.1.1.1@tcp:/lustrefs\" at \"error_mount_sens_mountflags\": fake MountSensitiveWithoutSystemdWithMountFlags: target error"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Error volume operation in progress",
			setup: func(d *Driver) {
				d.volumeLocks.TryAcquire(lockKey)
			},
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#testSubDir",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir},
				Readonly:         false,
			},
			expectedErr:          status.Error(codes.Aborted, fmt.Sprintf(volumeOperationAlreadyExistsFmt, "vol_1#lustrefs#1.1.1.1#testSubDir")),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
			cleanup: func(d *Driver) {
				d.volumeLocks.Release(lockKey)
			},
		},
		{
			desc: "Error failed ping to MGS",
			setup: func(d *Driver) {
				d.pingChecker = &fakePingChecker{err: status.Error(codes.FailedPrecondition, "MGS IP address is not reachable")}
			},
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
			},
			expectedErr:          status.Error(codes.FailedPrecondition, "MGS IP address is not reachable"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
	}

	for i := range tests {
		d := NewFakeDriver(t)
		test := &tests[i]

		fakeMounter := &fakeMounter{}
		preserveFakeUnmountedData(t, fakeMounter)
		fakeExec := &testingexec.FakeExec{ExactOrder: true}
		d.mounter = &mount.SafeFormatAndMount{
			Interface: fakeMounter,
			Exec:      fakeExec,
		}
		forceMounter, ok := d.mounter.Interface.(mount.MounterForceUnmounter)
		require.True(t, ok, "Mounter should implement MounterForceUnmounter")
		d.forceMounter = &forceMounter
		d.workingMountDir = workingMountDir
		err := makeDir(targetTest)
		require.NoError(t, err)
		err = makeDir(alreadyExistTarget)
		require.NoError(t, err)

		if test.setup != nil {
			test.setup(d)
		}

		fakeMounter.ResetLog()

		t.Run(test.desc, func(t *testing.T) {
			_, err = d.NodePublishVolume(t.Context(), &test.req)
			require.ErrorIs(t, err, test.expectedErr)

			mountPoints, err := d.mounter.List()
			require.NoError(t, err)
			assert.Equal(t, test.expectedMountpoints, mountPoints, "Desc: %s - Incorrect mount points: %v - Expected: %v", test.desc, mountPoints, test.expectedMountpoints)
			mountActions := fakeMounter.GetLog()
			assert.Equal(t, test.expectedMountActions, mountActions, "Desc: %s - Incorrect mount actions: %v - Expected: %v", test.desc, mountActions, test.expectedMountActions)
		})

		if test.cleanup != nil {
			test.cleanup(d)
		}

		err = d.mounter.Unmount(d.workingMountDir)
		require.NoError(t, err)
		err = os.RemoveAll(d.workingMountDir)
		require.NoError(t, err)
		err = d.mounter.Unmount(targetTest)
		require.NoError(t, err)
		err = os.RemoveAll(alreadyExistTarget)
		require.NoError(t, err)
		err = os.RemoveAll(targetTest)
		require.NoError(t, err)
	}
}

func TestNodeUnpublishVolume(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Errorf("failed to get current working directory")
	}

	workingMountDir := filepath.Join(workingDirectory, "workingMountDir")
	lockKey := fmt.Sprintf("%s-%s", "vol_1#lustrefs#1.1.1.1#testSubDir", targetTest)

	tests := []struct {
		desc                 string
		setup                func(*Driver)
		req                  csi.NodeUnpublishVolumeRequest
		expectedErr          error
		expectExistingSubDir bool
		expectUnmountedData  bool
		expectedMountpoints  []mount.MountPoint
		expectedMountActions []mount.FakeAction
		cleanup              func(*Driver)
	}{
		{
			desc:                 "Volume ID missing",
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest},
			expectedErr:          status.Error(codes.InvalidArgument, "Volume ID missing in request"),
			expectExistingSubDir: false,
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc:                 "Target missing",
			req:                  csi.NodeUnpublishVolumeRequest{VolumeId: "vol_1"},
			expectedErr:          status.Error(codes.InvalidArgument, "Target path missing in request"),
			expectExistingSubDir: false,
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc:                 "Cannot find mount point",
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1#testSubDir"},
			expectedErr:          nil,
			expectExistingSubDir: false,
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Valid request with old ID",
			setup: func(d *Driver) {
				volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}
				req := csi.NodePublishVolumeRequest{
					VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
					}},
					VolumeId:      "vol_1#lustrefs#1.1.1.1",
					TargetPath:    targetTest,
					VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
					Readonly:      false,
				}
				_, err := d.NodePublishVolume(t.Context(), &req)
				require.NoError(t, err)
			},
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1"},
			expectedErr:          nil,
			expectExistingSubDir: false,
			expectedMountpoints:  []mount.MountPoint{},
			expectedMountActions: []mount.FakeAction{
				{Action: "unmount", Target: "target_test", Source: "", FSType: ""},
			},
		},
		{
			desc: "Error volume operation in progress",
			setup: func(d *Driver) {
				d.volumeLocks.TryAcquire(lockKey)
			},
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1#testSubDir"},
			expectedErr:          status.Error(codes.Aborted, fmt.Sprintf(volumeOperationAlreadyExistsFmt, "vol_1#lustrefs#1.1.1.1#testSubDir")),
			expectExistingSubDir: false,
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
			cleanup: func(d *Driver) {
				d.volumeLocks.Release(lockKey)
			},
		},
		{
			desc: "Valid request with sub-dir",
			setup: func(d *Driver) {
				volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}
				req := csi.NodePublishVolumeRequest{
					VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
					}},
					VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir",
					TargetPath:    targetTest,
					VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir},
					Readonly:      false,
				}
				_, err := d.NodePublishVolume(t.Context(), &req)
				require.NoError(t, err)
			},
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1#testSubDir"},
			expectedErr:          nil,
			expectExistingSubDir: false,
			expectUnmountedData:  true,
			expectedMountpoints:  []mount.MountPoint{},
			expectedMountActions: []mount.FakeAction{
				{Action: "unmount", Target: "target_test", Source: "", FSType: ""},
			},
		},
		{
			desc: "Valid request with unexpected ID skips cleanup",
			setup: func(d *Driver) {
				err = makeDir(targetTest)
				require.NoError(t, err)
				err = makeDir(filepath.Join(workingMountDir, targetTest, subDir))
				require.NoError(t, err)
				err = d.mounter.Mount("1.1.1.1@tcp:/lustrefs/"+subDir, targetTest, "lustre", []string{"noatime", "flock"})
				require.NoError(t, err)
			},
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1"},
			expectedErr:          nil,
			expectExistingSubDir: true,
			expectedMountpoints:  []mount.MountPoint{},
			expectedMountActions: []mount.FakeAction{
				{Action: "unmount", Target: "target_test", Source: "", FSType: ""},
			},
		},
	}

	// Setup
	d := NewFakeDriver(t)
	d.workingMountDir = workingMountDir

	for i := range tests {
		test := &tests[i]
		fakeMounter := &fakeMounter{}
		backingDirectory := preserveFakeUnmountedData(t, fakeMounter)
		fakeExec := &testingexec.FakeExec{ExactOrder: true}
		d.mounter = &mount.SafeFormatAndMount{
			Interface: fakeMounter,
			Exec:      fakeExec,
		}
		forceMounter, ok := d.mounter.Interface.(mount.MounterForceUnmounter)
		require.True(t, ok, "Mounter should implement MounterForceUnmounter")
		d.forceMounter = &forceMounter
		err := makeDir(targetTest)
		require.NoError(t, err)

		if test.setup != nil {
			test.setup(d)
		}

		fakeMounter.ResetLog()

		t.Run(test.desc, func(t *testing.T) {
			_, err := d.NodeUnpublishVolume(t.Context(), &test.req)
			require.ErrorIs(t, err, test.expectedErr)
			mountPoints, err := d.mounter.List()
			require.NoError(t, err)
			assert.Equal(t, test.expectedMountpoints, mountPoints, "Desc: %s - Incorrect mount points: %v - Expected: %v", test.desc, mountPoints, test.expectedMountpoints)
			mountActions := fakeMounter.GetLog()
			assert.Equal(t, test.expectedMountActions, mountActions, "Desc: %s - Incorrect mount actions: %v - Expected: %v", test.desc, mountActions, test.expectedMountActions)
			internalMountDir := filepath.Join(d.workingMountDir, targetTest)
			if test.expectedErr == nil {
				subDirPath := filepath.Join(internalMountDir, subDir)

				if test.expectExistingSubDir {
					assert.DirExists(t, subDirPath, "Expected sub-dir %q to be created", subDirPath)
				} else {
					assert.NoDirExists(t, subDirPath, "Expected sub-dir %q not to exist", subDirPath)
				}
				if test.expectUnmountedData {
					assert.DirExists(t, filepath.Join(backingDirectory, "0", subDir), "unmount must preserve the filesystem's subdirectory")
				}
			}
			err = d.mounter.Unmount(internalMountDir)
			require.NoError(t, err)
			err = os.RemoveAll(internalMountDir)
			require.NoError(t, err)
		})
		if test.cleanup != nil {
			test.cleanup(d)
		}

		err = d.mounter.Unmount(d.workingMountDir)
		require.NoError(t, err)
		err = os.RemoveAll(d.workingMountDir)
		require.NoError(t, err)
		err = d.mounter.Unmount(targetTest)
		require.NoError(t, err)
		err = os.RemoveAll(targetTest)
		require.NoError(t, err)
	}
}

func makeDir(pathname string) error {
	err := os.MkdirAll(pathname, os.FileMode(0o755))
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func TestMakeDir(t *testing.T) {
	// Successfully create directory
	err := makeDir(targetTest)
	require.NoError(t, err)

	// Failed case
	err = makeDir("./azurelustre.go")
	require.ErrorAs(t, err, new(*os.PathError))

	// Remove the directory created
	err = os.RemoveAll(targetTest)
	require.NoError(t, err)
}

func NewSafeMounter() *mount.SafeFormatAndMount {
	return &mount.SafeFormatAndMount{
		Interface: mount.New(""),
	}
}

func TestNewSafeMounter(t *testing.T) {
	mounter := NewSafeMounter()
	require.NotNil(t, mounter)
	assert.NotNil(t, mounter.Interface)
}

func TestNodeGetVolumeStats(t *testing.T) {
	nonexistedPath := "/not/a/real/directory"
	fakePath := "/tmp/fake-volume-path"

	tests := []struct {
		desc        string
		req         csi.NodeGetVolumeStatsRequest
		expectedErr error
	}{
		{
			desc:        "[Error] Volume ID missing",
			req:         csi.NodeGetVolumeStatsRequest{VolumePath: targetTest},
			expectedErr: status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume ID was empty"),
		},
		{
			desc:        "[Error] VolumePath missing",
			req:         csi.NodeGetVolumeStatsRequest{VolumeId: "vol_1"},
			expectedErr: status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume path was empty"),
		},
		{
			desc:        "[Error] Incorrect volume path",
			req:         csi.NodeGetVolumeStatsRequest{VolumePath: nonexistedPath, VolumeId: "vol_1"},
			expectedErr: status.Errorf(codes.NotFound, "path /not/a/real/directory does not exist"),
		},
		{
			desc:        "[Success] Standard success",
			req:         csi.NodeGetVolumeStatsRequest{VolumePath: fakePath, VolumeId: "vol_1"},
			expectedErr: nil,
		},
	}

	// Setup
	d := NewFakeDriver(t)

	for i := range tests {
		test := &tests[i]
		err := makeDir(fakePath)
		require.NoError(t, err)

		defer func() {
			err = os.RemoveAll(fakePath)
			require.NoError(t, err)
		}()

		t.Run(test.desc, func(t *testing.T) {
			_, err := d.NodeGetVolumeStats(t.Context(), &test.req)
			require.ErrorIs(t, err, test.expectedErr)
		})
	}
}

func TestEnsureStrictSubpath(t *testing.T) {
	cases := []struct {
		desc           string
		subPath        string
		expectedResult bool
	}{
		{
			desc:           "valid subpath",
			subPath:        "subPath",
			expectedResult: true,
		},
		{
			desc:           "valid subpath, does not actually get to parent",
			subPath:        "subPath/../otherSubPath",
			expectedResult: true,
		},
		{
			desc:           "invalid subpath, leading slash",
			subPath:        "/subPath",
			expectedResult: false,
		},
		{
			desc:           "invalid subpath, attempts to go to parent",
			subPath:        "../subPath",
			expectedResult: false,
		},
		{
			desc:           "invalid subpath, same as parent",
			subPath:        "./",
			expectedResult: false,
		},
		{
			desc:           "invalid subpath, empty",
			subPath:        "",
			expectedResult: false,
		},
	}
	for _, test := range cases {
		t.Run(test.desc, func(t *testing.T) {
			actualResult := ensureStrictSubpath(test.subPath)

			assert.Equal(t, test.expectedResult, actualResult, "Desc: %s - Incorrect lustre volume: %v - Expected: %v", test.desc, actualResult, test.expectedResult)
		})
	}
}

func TestGetInternalVolumePath(t *testing.T) {
	cases := []struct {
		desc            string
		workingMountDir string
		mountPath       string
		subDirPath      string
		result          string
		expectedErr     error
	}{
		{
			desc:            "empty sub-dir",
			workingMountDir: "/tmp",
			mountPath:       targetTest,
			subDirPath:      "",
			result:          "",
			expectedErr:     status.Error(codes.InvalidArgument, "sub-dir \"\" must be strict subpath"),
		},
		{
			desc:            "valid sub-dir",
			workingMountDir: "/tmp",
			mountPath:       targetTest,
			subDirPath:      "testSubDir",
			result:          filepath.Join("/tmp", "target_test/testSubDir"),
			expectedErr:     nil,
		},
		{
			desc:            "valid volume with multiple sub-dir levels",
			workingMountDir: "/tmp",
			mountPath:       targetTest,
			subDirPath:      "testSubDir/nestedSubDir",
			result:          filepath.Join("/tmp", "target_test/testSubDir/nestedSubDir"),
			expectedErr:     nil,
		},
		{
			desc:            "invalid sub-dir that would go to parent dir",
			workingMountDir: "/tmp",
			mountPath:       targetTest,
			subDirPath:      "../testSubDir",
			result:          "",
			expectedErr:     status.Error(codes.InvalidArgument, "sub-dir \"../testSubDir\" must be strict subpath"),
		},
	}

	for _, test := range cases {
		t.Run(test.desc, func(t *testing.T) {
			path, err := getInternalVolumePath(test.workingMountDir, test.mountPath, test.subDirPath)
			require.ErrorIs(t, err, test.expectedErr)
			assert.Equal(t, test.result, path)
		})
	}
}

func TestGetInternalMountPath(t *testing.T) {
	cases := []struct {
		desc            string
		workingMountDir string
		mountPath       string
		result          string
		expectedErr     error
	}{
		{
			desc:            "empty mount path",
			workingMountDir: "/tmp",
			mountPath:       "",
			result:          "",
			expectedErr:     status.Error(codes.Internal, "invalid mount path \"\""),
		},
		{
			desc:            "valid mount path",
			workingMountDir: "/tmp",
			mountPath:       "/mount_path",
			result:          filepath.Join("/tmp", "mount_path"),
			expectedErr:     nil,
		},
		{
			desc:            "invalid path that would go to parent dir",
			workingMountDir: "/tmp",
			mountPath:       "../invalid",
			result:          "",
			expectedErr:     status.Error(codes.Internal, "invalid mount path \"../invalid\""),
		},
	}

	for _, test := range cases {
		t.Run(test.desc, func(t *testing.T) {
			path, err := getInternalMountPath(test.workingMountDir, test.mountPath)
			require.ErrorIs(t, err, test.expectedErr)
			assert.Equal(t, test.result, path)
		})
	}
}

func TestNewLustreVolume(t *testing.T) {
	cases := []struct {
		desc                 string
		id                   string
		volName              string
		params               map[string]string
		expectedLustreVolume *lustreVolume
		expectedErr          error
	}{
		{
			desc:    "valid context, no sub-dir",
			id:      "vol_1#lustrefs#1.1.1.1#",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
			},
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "",
			},
		},
		{
			desc:    "valid context with dynamic provisioning",
			id:      "vol_1#lustrefs#1.1.1.1##t#test-amlfilesystem-rg",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address":                  "1.1.1.1",
				"fs-name":                         "lustrefs",
				"amlfilesystem-name":              "test-amlfilesystem-name",
				"resource-group-name":             "test-amlfilesystem-rg",
				"created-by-dynamic-provisioning": "t",
			},
			expectedLustreVolume: &lustreVolume{
				id:                           "vol_1#lustrefs#1.1.1.1##t#test-amlfilesystem-rg",
				name:                         "vol_1",
				azureLustreName:              "lustrefs",
				mgsIPAddress:                 "1.1.1.1",
				subDir:                       "",
				resourceGroupName:            "test-amlfilesystem-rg",
				createdByDynamicProvisioning: true,
			},
		},
		{
			desc:    "valid context not created by dynamic provisioning",
			id:      "vol_1#lustrefs#1.1.1.1##f#test-amlfilesystem-rg",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address":                  "1.1.1.1",
				"fs-name":                         "lustrefs",
				"amlfilesystem-name":              "test-amlfilesystem-name",
				"resource-group-name":             "test-amlfilesystem-rg",
				"created-by-dynamic-provisioning": "f",
			},
			expectedLustreVolume: &lustreVolume{
				id:                           "vol_1#lustrefs#1.1.1.1##f#test-amlfilesystem-rg",
				name:                         "vol_1",
				azureLustreName:              "lustrefs",
				mgsIPAddress:                 "1.1.1.1",
				subDir:                       "",
				resourceGroupName:            "test-amlfilesystem-rg",
				createdByDynamicProvisioning: false,
			},
		},
		{
			desc:    "ignore invalid 'created by dynamic provisioning' value",
			id:      "vol_1#lustrefs#1.1.1.1##unknown#test-amlfilesystem-rg",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address":                  "1.1.1.1",
				"fs-name":                         "lustrefs",
				"amlfilesystem-name":              "test-amlfilesystem-name",
				"resource-group-name":             "test-amlfilesystem-rg",
				"created-by-dynamic-provisioning": "unknown",
			},
			expectedLustreVolume: &lustreVolume{
				id:                           "vol_1#lustrefs#1.1.1.1##unknown#test-amlfilesystem-rg",
				name:                         "vol_1",
				azureLustreName:              "lustrefs",
				mgsIPAddress:                 "1.1.1.1",
				subDir:                       "",
				resourceGroupName:            "test-amlfilesystem-rg",
				createdByDynamicProvisioning: false,
			},
		},
		{
			desc:    "dynamic provisioning empty string defaults to false",
			id:      "vol_1#lustrefs#1.1.1.1##f#test-amlfilesystem-rg",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address":                  "1.1.1.1",
				"fs-name":                         "lustrefs",
				"amlfilesystem-name":              "test-amlfilesystem-name",
				"resource-group-name":             "test-amlfilesystem-rg",
				"created-by-dynamic-provisioning": "",
			},
			expectedLustreVolume: &lustreVolume{
				id:                           "vol_1#lustrefs#1.1.1.1##f#test-amlfilesystem-rg",
				name:                         "vol_1",
				azureLustreName:              "lustrefs",
				mgsIPAddress:                 "1.1.1.1",
				subDir:                       "",
				resourceGroupName:            "test-amlfilesystem-rg",
				createdByDynamicProvisioning: false,
			},
		},
		{
			desc:    "invalid dynamic provisioning value 'true' defaults to false",
			id:      "vol_1#lustrefs#1.1.1.1##f#test-amlfilesystem-rg",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address":                  "1.1.1.1",
				"fs-name":                         "lustrefs",
				"amlfilesystem-name":              "test-amlfilesystem-name",
				"resource-group-name":             "test-amlfilesystem-rg",
				"created-by-dynamic-provisioning": "true",
			},
			expectedLustreVolume: &lustreVolume{
				id:                           "vol_1#lustrefs#1.1.1.1##f#test-amlfilesystem-rg",
				name:                         "vol_1",
				azureLustreName:              "lustrefs",
				mgsIPAddress:                 "1.1.1.1",
				subDir:                       "",
				resourceGroupName:            "test-amlfilesystem-rg",
				createdByDynamicProvisioning: false,
			},
		},
		{
			desc:    "case insensitive parameter key handling",
			id:      "vol_1#lustrefs#1.1.1.1##t#test-amlfilesystem-rg",
			volName: "vol_1",
			params: map[string]string{
				"MGS-IP-ADDRESS":                  "1.1.1.1",
				"FS-NAME":                         "lustrefs",
				"RESOURCE-GROUP-NAME":             "test-amlfilesystem-rg",
				"CREATED-BY-DYNAMIC-PROVISIONING": "t",
			},
			expectedLustreVolume: &lustreVolume{
				id:                           "vol_1#lustrefs#1.1.1.1##t#test-amlfilesystem-rg",
				name:                         "vol_1",
				azureLustreName:              "lustrefs",
				mgsIPAddress:                 "1.1.1.1",
				subDir:                       "",
				resourceGroupName:            "test-amlfilesystem-rg",
				createdByDynamicProvisioning: true,
			},
		},
		{
			desc:    "uppercase dynamic provisioning value 'T' is not case insensitive",
			id:      "vol_1#lustrefs#1.1.1.1##T#test-amlfilesystem-rg",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address":                  "1.1.1.1",
				"fs-name":                         "lustrefs",
				"resource-group-name":             "test-amlfilesystem-rg",
				"created-by-dynamic-provisioning": "T",
			},
			expectedLustreVolume: &lustreVolume{
				id:                           "vol_1#lustrefs#1.1.1.1##T#test-amlfilesystem-rg",
				name:                         "vol_1",
				azureLustreName:              "lustrefs",
				mgsIPAddress:                 "1.1.1.1",
				subDir:                       "",
				resourceGroupName:            "test-amlfilesystem-rg",
				createdByDynamicProvisioning: false,
			},
		},
		{
			desc:    "valid context with sub-dir",
			id:      "vol_1#lustrefs#1.1.1.1#testSubDir",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
				"sub-dir":        "testSubDir",
			},
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#testSubDir",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir",
			},
		},
		{
			desc:    "invalid parameter is ignored",
			id:      "vol_1#lustrefs#1.1.1.1#",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address":    "1.1.1.1",
				"fs-name":           "lustrefs",
				"invalid-parameter": "value",
			},
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "",
			},
			expectedErr: nil,
		},
		{
			desc:    "incorrect volume id is ignored for context",
			id:      "vol_1#otherfs#2.2.2.2#otherSubDir",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
				"sub-dir":        "testSubDir",
			},
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#otherfs#2.2.2.2#otherSubDir",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir",
			},
			expectedErr: nil,
		},
		{
			desc:    "sub-dir cannot be empty",
			id:      "vol_1#lustrefs#1.1.1.1#",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
				"sub-dir":        "",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Context sub-dir must not be empty or root if provided"),
		},
		{
			desc:    "sub-dir cannot be root",
			id:      "vol_1#lustrefs#1.1.1.1#/",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
				"sub-dir":        "/",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Context sub-dir must not be empty or root if provided"),
		},
		{
			desc:    "MGS address cannot be a hostname",
			id:      "vol_1#lustrefs#mgs.example.com#",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address": "mgs.example.com",
				"fs-name":        "lustrefs",
			},
			expectedErr: status.Error(codes.InvalidArgument, `Context mgs-ip-address "mgs.example.com" must be a valid IPv4 address`),
		},
		{
			desc:    "MGS address cannot be an option",
			id:      "vol_1#lustrefs#--help#",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address": "--help",
				"fs-name":        "lustrefs",
			},
			expectedErr: status.Error(codes.InvalidArgument, `Context mgs-ip-address "--help" must be a valid IPv4 address`),
		},
		{
			desc:    "MGS address cannot contain whitespace",
			id:      "vol_1#lustrefs#1.1.1.1 #",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1 ",
				"fs-name":        "lustrefs",
			},
			expectedErr: status.Error(codes.InvalidArgument, `Context mgs-ip-address "1.1.1.1 " must be a valid IPv4 address`),
		},
		{
			desc:    "MGS address cannot be IPv6",
			id:      "vol_1#lustrefs#2001:db8::1#",
			volName: "vol_1",
			params: map[string]string{
				"mgs-ip-address": "2001:db8::1",
				"fs-name":        "lustrefs",
			},
			expectedErr: status.Error(codes.InvalidArgument, `Context mgs-ip-address "2001:db8::1" must be a valid IPv4 address`),
		},
	}

	for _, test := range cases {
		t.Run(test.desc, func(t *testing.T) {
			vol, err := newLustreVolume(test.id, test.volName, test.params)
			require.ErrorIs(t, err, test.expectedErr)
			assert.Equal(t, test.expectedLustreVolume, vol, "Desc: %s - Incorrect lustre volume: %v - Expected: %v", test.desc, vol, test.expectedLustreVolume)
		})
	}
}

func TestNodeStageVolume(t *testing.T) {
	d := NewFakeDriver(t)
	req := csi.NodeStageVolumeRequest{}
	resp, err := d.NodeStageVolume(t.Context(), &req)
	assert.Nil(t, resp)
	require.ErrorContains(t, err, "not implemented")
	assert.Equal(t, codes.Unimplemented, status.Code(err))
}

func TestNodeUnstageVolume(t *testing.T) {
	d := NewFakeDriver(t)
	req := csi.NodeUnstageVolumeRequest{}
	resp, err := d.NodeUnstageVolume(t.Context(), &req)
	assert.Nil(t, resp)
	require.ErrorContains(t, err, "not implemented")
	assert.Equal(t, codes.Unimplemented, status.Code(err))
}
