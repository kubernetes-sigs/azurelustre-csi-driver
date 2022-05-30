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
	"os"
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

// TODO_JUSJIN: update and add tests

const (
	sourceTest = "./source_test"
	targetTest = "./target_test"
)

func TestNodeGetInfo(t *testing.T) {
	d := NewFakeDriver()

	// Test valid request
	req := csi.NodeGetInfoRequest{}
	resp, err := d.NodeGetInfo(context.Background(), &req)
	assert.NoError(t, err)
	assert.Equal(t, resp.GetNodeId(), fakeNodeID)
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
	assert.Equal(t, resp.Capabilities[0].GetType(), capType)
	assert.NoError(t, err)
}

func TestEnsureMountPoint(t *testing.T) {
	errorTarget := "./error_is_likely_target"
	alreadyExistTarget := "./false_is_likely_exist_target"
	falseTarget := "./false_is_likely_target"
	//azureFile := "./azure.go"

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
		//{
		//	desc:        "[Error] Not a directory",
		//	target:      azureFile,
		//	expectedErr: &os.PathError{Op: "mkdir", Path: "./azure.go", Err: syscall.ENOTDIR},
		//},
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
	_ = makeDir(alreadyExistTarget)
	d := NewFakeDriver()
	fakeMounter := &fakeMounter{}
	fakeExec := &testingexec.FakeExec{ExactOrder: true}
	d.mounter = &mount.SafeFormatAndMount{
		Interface: fakeMounter,
		Exec:      fakeExec,
	}

	for _, test := range tests {
		_, err := d.ensureMountPoint(test.target)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("[%s]: Unexpected Error: %v, expected error: %v", test.desc, err, test.expectedErr)
		}
	}

	// Clean up
	err := os.RemoveAll(alreadyExistTarget)
	assert.NoError(t, err)
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
}

func TestNodePublishVolume(t *testing.T) {
	volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}
	//createDirError := status.Errorf(codes.Internal,
	//	"Could not mount target \"./azure.go\": mkdir ./azure.go: not a directory")
	tests := []struct {
		desc        string
		setup       func(*Driver)
		req         csi.NodePublishVolumeRequest
		expectedErr error
		cleanup     func(*Driver)
	}{
		{
			desc:        "Volume capabilities missing",
			req:         csi.NodePublishVolumeRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume capability missing in request"),
		},
		{
			desc:        "Volume ID missing",
			req:         csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap}},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID missing in request"),
		},
		{
			desc: "Stage target path missing",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:          "vol_1",
				StagingTargetPath: sourceTest},
			expectedErr: status.Error(codes.InvalidArgument, "Target path not provided"),
		},
		{
			desc: "Valid request read only",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				VolumeContext:     map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
				Readonly:          true},
			expectedErr: nil,
		},
		{
			desc: "Valid mount options",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
			}},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				VolumeContext:     map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
				Readonly:          false},
			expectedErr: nil,
		},
		{
			desc: "Valid mount options with readonly",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock"}},
			}},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				VolumeContext:     map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
				Readonly:          true},
			expectedErr: nil,
		},
		{
			desc: "Valid mount options with duplicated readonly",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"noatime", "flock", "ro"}},
			}},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				VolumeContext:     map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
				Readonly:          true},
			expectedErr: nil,
		},
		//{
		//	desc: "Error creating directory",
		//	req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
		//		VolumeId:          "vol_1",
		//		TargetPath:        "./azure.go",
		//		StagingTargetPath: sourceTest,
		//		Readonly:          true},
		//	expectedErr: createDirError,
		//},
		{
			desc: "Error mounting resource busy",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				VolumeContext:     map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
				Readonly:          true},
			expectedErr: nil,
		},
	}

	// Setup
	_ = makeDir(sourceTest)
	_ = makeDir(targetTest)
	d := NewFakeDriver()
	fakeMounter := &fakeMounter{}
	fakeExec := &testingexec.FakeExec{ExactOrder: true}
	d.mounter = &mount.SafeFormatAndMount{
		Interface: fakeMounter,
		Exec:      fakeExec,
	}

	for _, test := range tests {
		if test.setup != nil {
			test.setup(d)
		}
		_, err := d.NodePublishVolume(context.Background(), &test.req)

		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Desc: %s - Unexpected error: %v - Expected: %v", test.desc, err, test.expectedErr)
		}
		if test.cleanup != nil {
			test.cleanup(d)
		}
	}

	// Clean up
	_ = d.mounter.Unmount(sourceTest)
	err := d.mounter.Unmount(targetTest)
	assert.NoError(t, err)
	err = os.RemoveAll(sourceTest)
	assert.NoError(t, err)
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
}

func TestNodeUnpublishVolume(t *testing.T) {
	tests := []struct {
		desc        string
		setup       func(*Driver)
		req         csi.NodeUnpublishVolumeRequest
		expectedErr error
		cleanup     func(*Driver)
	}{
		{
			desc:        "Volume ID missing",
			req:         csi.NodeUnpublishVolumeRequest{TargetPath: targetTest},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID missing in request"),
		},
		{
			desc:        "Target missing",
			req:         csi.NodeUnpublishVolumeRequest{VolumeId: "vol_1"},
			expectedErr: status.Error(codes.InvalidArgument, "Target path missing in request"),
		},
		{
			desc:        "Valid request",
			req:         csi.NodeUnpublishVolumeRequest{TargetPath: "./abc.go", VolumeId: "vol_1"},
			expectedErr: nil,
		},
	}

	// Setup
	_ = makeDir(sourceTest)
	_ = makeDir(targetTest)
	d := NewFakeDriver()

	fakeMounter := &fakeMounter{}
	fakeExec := &testingexec.FakeExec{ExactOrder: true}
	d.mounter = &mount.SafeFormatAndMount{
		Interface: fakeMounter,
		Exec:      fakeExec,
	}

	for _, test := range tests {
		if test.setup != nil {
			test.setup(d)
		}
		_, err := d.NodeUnpublishVolume(context.Background(), &test.req)

		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
		if test.cleanup != nil {
			test.cleanup(d)
		}
	}

	//Clean up
	err := d.mounter.Unmount(targetTest)
	assert.NoError(t, err)
	err = os.RemoveAll(sourceTest)
	assert.NoError(t, err)
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
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
	//Successfully create directory
	err := makeDir(targetTest)
	assert.NoError(t, err)

	//Failed case
	//err = makeDir("./azure.go")
	//var e *os.PathError
	//if !errors.As(err, &e) {
	//	t.Errorf("Unexpected Error: %v", err)
	//}

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
	_ = makeDir(fakePath)
	d := NewFakeDriver()

	for _, test := range tests {
		_, err := d.NodeGetVolumeStats(context.Background(), &test.req)
		//t.Errorf("[debug] error: %v\n metrics: %v", err, metrics)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %v, expected error: %v, actual error: %v", test.desc, test.expectedErr, err)
		}
	}

	// Clean up
	err := os.RemoveAll(fakePath)
	assert.NoError(t, err)
}
