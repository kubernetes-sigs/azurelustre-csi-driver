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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"

	mount "k8s.io/mount-utils"
	testingexec "k8s.io/utils/exec/testing"
)

const (
	targetTest = "target_test"
	subDir     = "testSubDir"
)

func TestNodeGetInfo(t *testing.T) {
	d := NewFakeDriver()

	// Test valid request
	req := csi.NodeGetInfoRequest{}
	resp, err := d.NodeGetInfo(context.Background(), &req)
	assert.NoError(t, err)
	assert.Equal(t, fakeNodeID, resp.GetNodeId())
}

func TestNodeGetCapabilities(t *testing.T) {
	d := NewFakeDriver()
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
	resp, err := d.NodeGetCapabilities(context.Background(), &req)
	assert.NotNil(t, resp)
	assert.Equal(t, capType, resp.Capabilities[0].GetType())
	assert.NoError(t, err)
}

func TestEnsureMountPoint(t *testing.T) {
	errorTarget := "./error_is_likely_target"
	alreadyExistTarget := "./false_is_likely_exist_target"
	falseTarget := "./false_is_likely_target"
	azureFile := "./azurelustre.go"

	tests := []struct {
		desc        string
		target      string
		expectedErr error
	}{
		{
			desc:        "[Error] Mocked by IsLikelyNotMountPoint",
			target:      errorTarget,
			expectedErr: fmt.Errorf("fake IsLikelyNotMountPoint: fake error"),
		},
		{
			desc:        "[Error] Error opening file",
			target:      falseTarget,
			expectedErr: &os.PathError{Op: "open", Path: "./false_is_likely_target", Err: syscall.ENOENT},
		},
		{
			desc:        "[Error] Not a directory",
			target:      azureFile,
			expectedErr: &os.PathError{Op: "mkdir", Path: "./azurelustre.go", Err: syscall.ENOTDIR},
		},
		{
			desc:        "[Success] Successful run",
			target:      targetTest,
			expectedErr: nil,
		},
		{
			desc:        "[Success] Already existing mount",
			target:      alreadyExistTarget,
			expectedErr: nil,
		},
	}

	// Setup
	d := NewFakeDriver()
	fakeMounter := &fakeMounter{}
	fakeExec := &testingexec.FakeExec{ExactOrder: true}
	d.mounter = &mount.SafeFormatAndMount{
		Interface: fakeMounter,
		Exec:      fakeExec,
	}

	for _, test := range tests {
		test := test // pin
		_ = makeDir(alreadyExistTarget)

		t.Run(test.desc, func(t *testing.T) {
			_, err := d.ensureMountPoint(test.target)
			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("Desc: %v, Expected error: %v, Actual error: %v", test.desc, test.expectedErr, err)
			}
		})

		err := os.RemoveAll(alreadyExistTarget)
		assert.NoError(t, err)
		err = os.RemoveAll(targetTest)
		assert.NoError(t, err)
	}
}

func TestNodePublishVolume(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Errorf("failed to get current working directory")
	}

	workingMountDir := filepath.Join(workingDirectory, "workingMountDir")

	volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}
	alreadyExistTarget := "./false_is_likely_exist_target"
	metadataVolumeID := "vol_1#lustrefs#1.1.1.1#testNestedSubDir/${pod.metadata.name}/${pod.metadata.namespace}/${pod.metadata.uid}/${serviceAccount.metadata.name}/${pvc.metadata.name}/${pvc.metadata.namespace}/${pv.metadata.name}/testNestedSubDir#false"
	createDirError := status.Errorf(codes.Internal,
		"Could not mount target \"./azurelustre.go\": mkdir ./azurelustre.go: not a directory")
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
			desc: "FS name missing",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1##1.1.1.1",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1"},
			},
			expectedErr:          status.Error(codes.InvalidArgument, "Context fs-name must be provided"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Invalid retain-sub-dir",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#testSubDir#f",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "f"},
			},
			expectedErr:          status.Error(codes.InvalidArgument, "Context retain-sub-dir value must be either true or false"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
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
				VolumeId:         "vol_1#lustrefs#1.1.1.1##false",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": "", "retain-sub-dir": "false"},
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
				VolumeId:         "vol_1#lustrefs#1.1.1.1#/#false",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": "/", "retain-sub-dir": "false"},
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
				VolumeId:         "vol_1#lustrefs#1.1.1.1#../../parentAttemptSubDir#false",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": "../../parentAttemptSubDir", "retain-sub-dir": "false"},
				Readonly:         false,
			},
			expectedErr:          status.Error(codes.InvalidArgument, "Context sub-dir must be strict subpath"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Invalid attempt to delete sub-dir on read only mount",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir#false",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "false"},
				Readonly:      true,
			},
			expectedErr:          status.Error(codes.InvalidArgument, "Context retain-sub-dir must be true for a sub-dir on a read-only volume"),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "Valid request read only",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir#true",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "true"},
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
				VolumeId:      "vol_1#lustrefs#1.1.1.1##true",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
				Readonly:      false,
			},
			expectedErr:          nil,
			expectedMountpoints:  []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"}},
		},
		{
			desc: "Valid mount options with sub-dir cleanup",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir#false",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "false"},
				Readonly:      false,
			},
			expectedErr:         nil,
			expectedMountpoints: []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs/testSubDir", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{
				{Action: "mount", Target: workingMountDir + "/vol_1#lustrefs#1.1.1.1#testSubDir#false", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"},
				{Action: "unmount", Target: workingMountDir + "/vol_1#lustrefs#1.1.1.1#testSubDir#false", Source: "", FSType: ""},
				{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs/testSubDir", FSType: "lustre"},
			},
		},
		{
			desc: "Valid mount options with slashes in paths",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir#false",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs/", "sub-dir": "/testSubDir/", "retain-sub-dir": "false"},
				Readonly:      false,
			},
			expectedErr:         nil,
			expectedMountpoints: []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs/testSubDir", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{
				{Action: "mount", Target: workingMountDir + "/vol_1#lustrefs#1.1.1.1#testSubDir#false", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"},
				{Action: "unmount", Target: workingMountDir + "/vol_1#lustrefs#1.1.1.1#testSubDir#false", Source: "", FSType: ""},
				{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs/testSubDir", FSType: "lustre"},
			},
		},
		{
			desc: "Valid mount options with readonly",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir#true",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "true"},
				Readonly:      true,
			},
			expectedErr:          nil,
			expectedMountpoints:  []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs/testSubDir", Path: "target_test", Type: "lustre", Opts: []string{"ro", "noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs/testSubDir", FSType: "lustre"}},
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
					"retain-sub-dir":                         "false",
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
				{Action: "mount", Target: filepath.Join(workingMountDir, metadataVolumeID), Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"},
				{Action: "unmount", Target: filepath.Join(workingMountDir, metadataVolumeID), Source: "", FSType: ""},
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
				VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir#true",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "true"},
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
				VolumeId:         "vol_1#lustrefs#1.1.1.1##true",
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
				err = makeDir(workingMountDir + "/false_is_likely#lustrefs#1.1.1.1#testSubDir#false")
				assert.NoError(t, err)
				err = d.mounter.Mount("1.1.1.1@tcp:/lustrefs/existing", workingMountDir+"/false_is_likely#lustrefs#1.1.1.1#testSubDir#false", "lustre", []string{"noatime", "flock"})
				assert.NoError(t, err)
			},
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
				}},
				VolumeId:      "false_is_likely#lustrefs#1.1.1.1#testSubDir#false",
				TargetPath:    targetTest,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "false"},
				Readonly:      false,
			},
			expectedErr:         nil,
			expectedMountpoints: []mount.MountPoint{{Device: "1.1.1.1@tcp:/lustrefs/testSubDir", Path: "target_test", Type: "lustre", Opts: []string{"noatime", "flock"}}},
			expectedMountActions: []mount.FakeAction{
				{Action: "unmount", Target: workingMountDir + "/false_is_likely#lustrefs#1.1.1.1#testSubDir#false", Source: "", FSType: ""},
				{Action: "mount", Target: workingMountDir + "/false_is_likely#lustrefs#1.1.1.1#testSubDir#false", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"},
				{Action: "unmount", Target: workingMountDir + "/false_is_likely#lustrefs#1.1.1.1#testSubDir#false", Source: "", FSType: ""},
				{Action: "mount", Target: "target_test", Source: "1.1.1.1@tcp:/lustrefs/testSubDir", FSType: "lustre"},
			},
		},
		{
			desc: "Success already mounted",
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#testSubDir#true",
				TargetPath:       alreadyExistTarget,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "true"},
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
				VolumeId:         "vol_1#lustrefs#1.1.1.1##true",
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
				d.volumeLocks.TryAcquire("vol_1#lustrefs#1.1.1.1#testSubDir#false")
			},
			req: csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:         "vol_1#lustrefs#1.1.1.1#testSubDir#false",
				TargetPath:       targetTest,
				VolumeContext:    map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "false"},
				Readonly:         false,
			},
			expectedErr:          status.Error(codes.Aborted, fmt.Sprintf(volumeOperationAlreadyExistsFmt, "vol_1#lustrefs#1.1.1.1#testSubDir#false")),
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
			cleanup: func(d *Driver) {
				d.volumeLocks.Release("vol_1#lustrefs#1.1.1.1#testSubDir#false")
			},
		},
	}

	// Setup
	d := NewFakeDriver()

	d.workingMountDir = workingMountDir

	for _, test := range tests {
		test := test // pin
		fakeMounter := &fakeMounter{}
		fakeExec := &testingexec.FakeExec{ExactOrder: true}
		d.mounter = &mount.SafeFormatAndMount{
			Interface: fakeMounter,
			Exec:      fakeExec,
		}
		_ = makeDir(targetTest)
		_ = makeDir(alreadyExistTarget)

		if test.setup != nil {
			test.setup(d)
		}

		fakeMounter.ResetLog()

		t.Run(test.desc, func(t *testing.T) {
			_, err := d.NodePublishVolume(context.Background(), &test.req)
			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("Desc: %v, Expected error: %v, Actual error: %v", test.desc, test.expectedErr, err)
			}

			mountPoints, _ := d.mounter.List()
			assert.Equal(t, test.expectedMountpoints, mountPoints, "Desc: %s - Incorrect mount points: %v - Expected: %v", test.desc, mountPoints, test.expectedMountpoints)
			mountActions := fakeMounter.GetLog()
			assert.Equal(t, test.expectedMountActions, mountActions, "Desc: %s - Incorrect mount actions: %v - Expected: %v", test.desc, mountActions, test.expectedMountActions)

			// Check that sub-dir has been created in the mount. This works because
			// the contents in workingMountDir still exist after the test. The reason is
			// os.Remove on workingMountDir fails because it is non-empty after unmount
			// since it's not a real mounted Lustre
			if subDirPath, ok := test.req.PublishContext["sub-dir"]; ok {
				if test.expectedErr == nil {
					internalMountDir := filepath.Join(d.workingMountDir, test.req.VolumeId)
					subDirPath := filepath.Join(internalMountDir, subDirPath)
					assert.DirExists(t, subDirPath, "Expected sub-dir %q to be created", subDirPath)
					err = d.mounter.Unmount(internalMountDir)
					assert.NoError(t, err)
					err = os.RemoveAll(internalMountDir)
					assert.NoError(t, err)
				}
			}
		})

		if test.cleanup != nil {
			test.cleanup(d)
		}

		err = d.mounter.Unmount(d.workingMountDir)
		assert.NoError(t, err)
		err = os.RemoveAll(d.workingMountDir)
		assert.NoError(t, err)
		err = d.mounter.Unmount(targetTest)
		assert.NoError(t, err)
		err = os.RemoveAll(alreadyExistTarget)
		assert.NoError(t, err)
		err = os.RemoveAll(targetTest)
		assert.NoError(t, err)
	}
}

func TestNodeUnpublishVolume(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Errorf("failed to get current working directory")
	}

	workingMountDir := filepath.Join(workingDirectory, "workingMountDir")

	tests := []struct {
		desc                 string
		setup                func(*Driver)
		req                  csi.NodeUnpublishVolumeRequest
		expectedErr          error
		expectExistingSubDir bool
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
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1#testSubDir#false"},
			expectedErr:          nil,
			expectExistingSubDir: false,
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
		},
		{
			desc: "sub-dir is not subpath of source, ignores sub-dir cleanup",
			setup: func(d *Driver) {
				err = makeDir(targetTest)
				assert.NoError(t, err)
				err = d.mounter.Mount("/otherPath", targetTest, "lustre", []string{"noatime", "flock"})
				assert.NoError(t, err)
			},
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1#.#false"},
			expectedErr:          nil,
			expectExistingSubDir: false,
			expectedMountpoints:  []mount.MountPoint{},
			expectedMountActions: []mount.FakeAction{
				{Action: "unmount", Target: "target_test", Source: "", FSType: ""},
			},
		},
		{
			desc: "sub-dir is not strict subpath, ignores sub-dir cleanup",
			setup: func(d *Driver) {
				err = makeDir(targetTest)
				assert.NoError(t, err)
				err = d.mounter.Mount("1.1.1.1@tcp:/lustrefs", targetTest, "lustre", []string{"noatime", "flock"})
				assert.NoError(t, err)
			},
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1#.#false"},
			expectedErr:          nil,
			expectExistingSubDir: false,
			expectedMountpoints:  []mount.MountPoint{},
			expectedMountActions: []mount.FakeAction{
				{Action: "unmount", Target: "target_test", Source: "", FSType: ""},
			},
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
				_, err := d.NodePublishVolume(context.Background(), &req)
				assert.NoError(t, err)
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
				d.volumeLocks.TryAcquire("vol_1#lustrefs#1.1.1.1#testSubDir#false")
			},
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1#testSubDir#false"},
			expectedErr:          status.Error(codes.Aborted, fmt.Sprintf(volumeOperationAlreadyExistsFmt, "vol_1#lustrefs#1.1.1.1#testSubDir#false")),
			expectExistingSubDir: false,
			expectedMountpoints:  nil,
			expectedMountActions: []mount.FakeAction{},
			cleanup: func(d *Driver) {
				d.volumeLocks.Release("vol_1#lustrefs#1.1.1.1#testSubDir#false")
			},
		},
		{
			desc: "Attempt to clean sub-dir on read-only volume",
			setup: func(d *Driver) {
				internalMountDir := filepath.Join(d.workingMountDir, "vol_1#lustrefs#1.1.1.1#testSubDir#false")
				subDirPath := filepath.Join(internalMountDir, subDir)
				err = makeDir(subDirPath)
				assert.NoError(t, err)
				err = makeDir(targetTest)
				assert.NoError(t, err)
				err = d.mounter.Mount("1.1.1.1@tcp:/lustrefs/testSubDir", targetTest, "lustre", []string{"ro", "noatime", "flock"})
				assert.NoError(t, err)
			},
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1#testSubDir#false"},
			expectedErr:          nil,
			expectExistingSubDir: true,
			expectedMountpoints:  []mount.MountPoint{},
			expectedMountActions: []mount.FakeAction{
				{Action: "unmount", Target: "target_test", Source: "", FSType: ""},
			},
		},
		{
			desc: "Valid request without sub-dir cleanup",
			setup: func(d *Driver) {
				volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}
				req := csi.NodePublishVolumeRequest{
					VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
					}},
					VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir#true",
					TargetPath:    targetTest,
					VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "true"},
					Readonly:      false,
				}
				_, err := d.NodePublishVolume(context.Background(), &req)
				assert.NoError(t, err)
			},
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1#testSubDir#true"},
			expectedErr:          nil,
			expectExistingSubDir: true,
			expectedMountpoints:  []mount.MountPoint{},
			expectedMountActions: []mount.FakeAction{
				{Action: "unmount", Target: "target_test", Source: "", FSType: ""},
			},
		},
		{
			desc: "Valid request with sub-dir cleanup",
			setup: func(d *Driver) {
				volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}
				req := csi.NodePublishVolumeRequest{
					VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
					}},
					VolumeId:      "vol_1#lustrefs#1.1.1.1#testSubDir#false",
					TargetPath:    targetTest,
					VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": subDir, "retain-sub-dir": "false"},
					Readonly:      false,
				}
				_, err := d.NodePublishVolume(context.Background(), &req)
				assert.NoError(t, err)
			},
			req:                  csi.NodeUnpublishVolumeRequest{TargetPath: targetTest, VolumeId: "vol_1#lustrefs#1.1.1.1#testSubDir#false"},
			expectedErr:          nil,
			expectExistingSubDir: false,
			expectedMountpoints:  []mount.MountPoint{},
			expectedMountActions: []mount.FakeAction{
				{Action: "unmount", Target: "target_test", Source: "", FSType: ""},
				{Action: "mount", Target: workingMountDir + "/vol_1#lustrefs#1.1.1.1#testSubDir#false", Source: "1.1.1.1@tcp:/lustrefs", FSType: "lustre"},
				{Action: "unmount", Target: workingMountDir + "/vol_1#lustrefs#1.1.1.1#testSubDir#false", Source: "", FSType: ""},
			},
		},
	}

	// Setup
	d := NewFakeDriver()
	d.workingMountDir = workingMountDir

	for _, test := range tests {
		test := test // pin
		fakeMounter := &fakeMounter{}
		fakeExec := &testingexec.FakeExec{ExactOrder: true}
		d.mounter = &mount.SafeFormatAndMount{
			Interface: fakeMounter,
			Exec:      fakeExec,
		}
		_ = makeDir(targetTest)

		if test.setup != nil {
			test.setup(d)
		}

		fakeMounter.ResetLog()

		t.Run(test.desc, func(t *testing.T) {
			_, err := d.NodeUnpublishVolume(context.Background(), &test.req)
			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("Desc: %v, Expected error: %v, Actual error: %v", test.desc, test.expectedErr, err)
			}
			mountPoints, _ := d.mounter.List()
			assert.Equal(t, test.expectedMountpoints, mountPoints, "Desc: %s - Incorrect mount points: %v - Expected: %v", test.desc, mountPoints, test.expectedMountpoints)
			mountActions := fakeMounter.GetLog()
			assert.Equal(t, test.expectedMountActions, mountActions, "Desc: %s - Incorrect mount actions: %v - Expected: %v", test.desc, mountActions, test.expectedMountActions)
			internalMountDir := filepath.Join(d.workingMountDir, test.req.VolumeId)
			if test.expectedErr == nil {
				subDirPath := filepath.Join(internalMountDir, subDir)
				if test.expectExistingSubDir {
					assert.DirExists(t, subDirPath, "Expected sub-dir %q to be retained", subDirPath)
				} else {
					assert.NoDirExists(t, subDirPath, "Expected sub-dir %q to be deleted", internalMountDir)
				}
			}
			err = d.mounter.Unmount(internalMountDir)
			assert.NoError(t, err)
			err = os.RemoveAll(internalMountDir)
			assert.NoError(t, err)
		})
		if test.cleanup != nil {
			test.cleanup(d)
		}

		err = d.mounter.Unmount(d.workingMountDir)
		assert.NoError(t, err)
		err = os.RemoveAll(d.workingMountDir)
		assert.NoError(t, err)
		err = d.mounter.Unmount(targetTest)
		assert.NoError(t, err)
		err = os.RemoveAll(targetTest)
		assert.NoError(t, err)
	}
}

func makeDir(pathname string) error {
	err := os.MkdirAll(pathname, os.FileMode(0755))
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
	assert.NoError(t, err)

	// Failed case
	err = makeDir("./azurelustre.go")
	var e *os.PathError
	if !errors.As(err, &e) {
		t.Errorf("Unexpected Error: %v", err)
	}

	// Remove the directory created
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
}

func NewSafeMounter() (*mount.SafeFormatAndMount, error) {
	return &mount.SafeFormatAndMount{
		Interface: mount.New(""),
	}, nil
}

func TestNewSafeMounter(t *testing.T) {
	resp, err := NewSafeMounter()
	assert.NotNil(t, resp)
	assert.Nil(t, err)
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
	d := NewFakeDriver()

	for _, test := range tests {
		_ = makeDir(fakePath)
		test := test // pin
		t.Run(test.desc, func(t *testing.T) {
			_, err := d.NodeGetVolumeStats(context.Background(), &test.req)
			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("Desc: %v, Expected error: %v, Actual error: %v", test.desc, test.expectedErr, err)
			}
		})

		err := os.RemoveAll(fakePath)
		assert.NoError(t, err)
	}
}

func TestEnsureStrictSubpath(t *testing.T) {
	cases := []struct {
		desc           string
		basePath       string
		subPath        string
		expectedErr    error
		expectedResult bool
	}{
		{
			desc:           "valid subpath",
			basePath:       "/basePath",
			subPath:        "subPath",
			expectedErr:    nil,
			expectedResult: true,
		},
		{
			desc:           "valid subpath with leading slash",
			basePath:       "/basePath",
			subPath:        "/subPath",
			expectedErr:    nil,
			expectedResult: true,
		},
		{
			desc:           "valid subpath, does not actually get to parent",
			basePath:       "/basePath",
			subPath:        "subPath/../otherSubPath",
			expectedErr:    nil,
			expectedResult: true,
		},
		{
			desc:           "invalid subpath, attempts to go to parent",
			basePath:       "/basePath",
			subPath:        "../subPath",
			expectedErr:    nil,
			expectedResult: false,
		},
		{
			desc:           "invalid subpath, same as parent",
			basePath:       "/basePath",
			subPath:        "./",
			expectedErr:    nil,
			expectedResult: false,
		},
		{
			desc:           "invalid subpath, empty",
			basePath:       "/basePath",
			subPath:        "",
			expectedErr:    nil,
			expectedResult: false,
		},
	}
	for _, test := range cases {
		test := test // pin
		t.Run(test.desc, func(t *testing.T) {
			actualResult, err := ensureStrictSubpath(test.basePath, test.subPath)

			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("Desc: %v, Expected error: %v, Actual error: %v", test.desc, test.expectedErr, err)
			}
			assert.Equal(t, test.expectedResult, actualResult, "Desc: %s - Incorrect lustre volume: %v - Expected: %v", test.desc, actualResult, test.expectedResult)
		})
	}
}

func TestGetLustreVolFromID(t *testing.T) {
	cases := []struct {
		desc                 string
		volumeID             string
		expectedLustreVolume *lustreVolume
		expectedErr          error
	}{
		{
			desc:     "correct volume id",
			volumeID: "vol_1#lustrefs#1.1.1.1#testSubDir#false",
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#testSubDir#false",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir",
				retainSubDir:    false,
			},
		},
		{
			desc:     "correct volume id with /",
			volumeID: "vol_1#lustrefs/#1.1.1.1#/testSubDir/#true",
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs/#1.1.1.1#/testSubDir/#true",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir",
				retainSubDir:    true,
			},
		},
		{
			desc:     "correct volume id with empty sub-dir",
			volumeID: "vol_1#lustrefs/#1.1.1.1##",
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs/#1.1.1.1##",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "",
				retainSubDir:    true,
			},
		},
		{
			desc:     "correct volume id with multiple sub-dir levels",
			volumeID: "vol_1#lustrefs#1.1.1.1#testSubDir/nestedSubDir#false",
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#testSubDir/nestedSubDir#false",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir/nestedSubDir",
				retainSubDir:    false,
			},
		},
		{
			desc:                 "incorrect volume id",
			volumeID:             "vol_1",
			expectedLustreVolume: nil,
			expectedErr:          errors.New("could not split volume id \"vol_1\" into lustre name and ip address"),
		},
		{
			desc:        "incorrect retain-sub-dir",
			volumeID:    "vol_1#lustrefs#1.1.1.1#testSubDir#f",
			expectedErr: errors.New("could not parse retain-sub-dir value \"f\" into boolean"),
		},
	}
	for _, test := range cases {
		test := test // pin
		t.Run(test.desc, func(t *testing.T) {
			lustreVolume, err := getLustreVolFromID(test.volumeID)

			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("Desc: %v, Expected error: %v, Actual error: %v", test.desc, test.expectedErr, err)
			}
			assert.Equal(t, test.expectedLustreVolume, lustreVolume, "Desc: %s - Incorrect lustre volume: %v - Expected: %v", test.desc, lustreVolume, test.expectedLustreVolume)
		})
	}
}

func TestGetInternalVolumePath(t *testing.T) {
	cases := []struct {
		desc            string
		workingMountDir string
		vol             *lustreVolume
		subDirPath      string
		result          string
		expectedErr     error
	}{
		{
			desc:            "nil volume",
			workingMountDir: "/tmp",
			vol:             nil,
			subDirPath:      "",
			result:          "",
			expectedErr:     status.Error(codes.Internal, "cannot get internal mount path for nil or empty volume"),
		},
		{
			desc:            "empty volume",
			workingMountDir: "/tmp",
			vol: &lustreVolume{
				id:              "",
				name:            "",
				azureLustreName: "",
				mgsIPAddress:    "",
				subDir:          "",
				retainSubDir:    false,
			},
			subDirPath:  "",
			result:      "",
			expectedErr: status.Error(codes.Internal, "cannot get internal mount path for nil or empty volume"),
		},
		{
			desc:            "valid volume",
			workingMountDir: "/tmp",
			vol: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#testSubDir#false",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir",
				retainSubDir:    false,
			},
			subDirPath:  "testSubDir",
			result:      filepath.Join("/tmp", "vol_1#lustrefs#1.1.1.1#testSubDir#false/testSubDir"),
			expectedErr: nil,
		},
		{
			desc:            "valid volume with multiple sub-dir levels",
			workingMountDir: "/tmp",
			vol: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#testSubDir/nestedSubDir#false",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir/nestedSubDir",
				retainSubDir:    false,
			},
			subDirPath:  "testSubDir/nestedSubDir",
			result:      filepath.Join("/tmp", "vol_1#lustrefs#1.1.1.1#testSubDir/nestedSubDir#false/testSubDir/nestedSubDir"),
			expectedErr: nil,
		},
		{
			desc:            "invalid sub-dir that would go to parent dir",
			workingMountDir: "/tmp",
			vol: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#../testSubDir#false",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "../testSubDir",
				retainSubDir:    false,
			},
			subDirPath:  "../testSubDir",
			result:      "",
			expectedErr: status.Error(codes.InvalidArgument, "sub-dir \"../testSubDir\" must be strict subpath"),
		},
	}

	for _, test := range cases {
		test := test // pin
		t.Run(test.desc, func(t *testing.T) {
			path, err := getInternalVolumePath(test.workingMountDir, test.vol, test.subDirPath)
			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("Desc: %v, Expected error: %v, Actual error: %v", test.desc, test.expectedErr, err)
			}
			assert.Equal(t, test.result, path)
		})
	}
}

func TestGetInternalMountPath(t *testing.T) {
	cases := []struct {
		desc            string
		workingMountDir string
		vol             *lustreVolume
		result          string
		expectedErr     error
	}{
		{
			desc:            "nil volume",
			workingMountDir: "/tmp",
			vol:             nil,
			result:          "",
			expectedErr:     status.Error(codes.Internal, "cannot get internal mount path for nil or empty volume"),
		},
		{
			desc:            "empty volume",
			workingMountDir: "/tmp",
			vol: &lustreVolume{
				id:              "",
				name:            "",
				azureLustreName: "",
				mgsIPAddress:    "",
				subDir:          "",
				retainSubDir:    false,
			},
			result:      "",
			expectedErr: status.Error(codes.Internal, "cannot get internal mount path for nil or empty volume"),
		},
		{
			desc:            "valid volume",
			workingMountDir: "/tmp",
			vol: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#testSubDir#false",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir",
				retainSubDir:    false,
			},
			result:      filepath.Join("/tmp", "vol_1#lustrefs#1.1.1.1#testSubDir#false"),
			expectedErr: nil,
		},
		{
			desc:            "invalid id that would go to parent dir",
			workingMountDir: "/tmp",
			vol: &lustreVolume{
				id:              "../../../vol_1#lustrefs#1.1.1.1#testSubDir#false",
				name:            "../../../vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir",
				retainSubDir:    false,
			},
			result:      "",
			expectedErr: status.Error(codes.InvalidArgument, "volume name or id \"../../../vol_1#lustrefs#1.1.1.1#testSubDir#false\" must be interpretable as a strict subpath"),
		},
	}

	for _, test := range cases {
		test := test // pin
		t.Run(test.desc, func(t *testing.T) {
			path, err := getInternalMountPath(test.workingMountDir, test.vol)
			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("Desc: %v, Expected error: %v, Actual error: %v", test.desc, test.expectedErr, err)
			}
			assert.Equal(t, test.result, path)
		})
	}
}

func TestNewLustreVolume(t *testing.T) {
	cases := []struct {
		desc                 string
		id                   string
		params               map[string]string
		expectedLustreVolume *lustreVolume
		expectedErr          error
	}{
		{
			desc: "valid context, no sub-dir",
			id:   "vol_1#lustrefs#1.1.1.1##true",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
			},
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1##true",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "",
				retainSubDir:    true,
			},
		},
		{
			desc: "valid context with sub-dir",
			id:   "vol_1#lustrefs#1.1.1.1#testSubDir#false",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
				"sub-dir":        "testSubDir",
				"retain-sub-dir": "false",
			},
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#testSubDir#false",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir",
				retainSubDir:    false,
			},
		},
		{
			desc: "invalid parameter is ignored",
			id:   "vol_1#lustrefs#1.1.1.1##true",
			params: map[string]string{
				"mgs-ip-address":    "1.1.1.1",
				"fs-name":           "lustrefs",
				"invalid-parameter": "value",
			},
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1##true",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "",
				retainSubDir:    true,
			},
			expectedErr: nil,
		},
		{
			desc: "incorrect volume id is ignored for context",
			id:   "vol_1#otherfs#2.2.2.2#otherSubDir#false",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
				"sub-dir":        "testSubDir",
				"retain-sub-dir": "true",
			},
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#testSubDir#true",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir",
				retainSubDir:    true,
			},
			expectedErr: nil,
		},
		{
			desc: "sub-dir cannot be empty",
			id:   "vol_1#lustrefs#1.1.1.1##false",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
				"sub-dir":        "",
				"retain-sub-dir": "false",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Context sub-dir must not be empty or root if provided"),
		},
		{
			desc: "sub-dir cannot be root",
			id:   "vol_1#lustrefs#1.1.1.1#/#false",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
				"sub-dir":        "/",
				"retain-sub-dir": "false",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Context sub-dir must not be empty or root if provided"),
		},
		{
			desc: "must have retain-sub-dir with sub-dir",
			id:   "vol_1#lustrefs#1.1.1.1#testSubDir#",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
				"sub-dir":        "testSubDir",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Context retain-sub-dir must be provided when sub-dir is provided"),
		},
		{
			desc: "sub-dir cannot be root",
			id:   "vol_1#lustrefs#1.1.1.1##false",
			params: map[string]string{
				"mgs-ip-address": "1.1.1.1",
				"fs-name":        "lustrefs",
				"retain-sub-dir": "false",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Context sub-dir must be provided when retain-sub-dir is provided"),
		},
	}

	for _, test := range cases {
		test := test // pin
		t.Run(test.desc, func(t *testing.T) {
			vol, err := newLustreVolume(test.id, test.params)
			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("[test: %s] Unexpected error: %v, expected error: %v", test.desc, err, test.expectedErr)
			}
			assert.Equal(t, test.expectedLustreVolume, vol, "Desc: %s - Incorrect lustre volume: %v - Expected: %v", test.desc, vol, test.expectedLustreVolume)
		})
	}
}
