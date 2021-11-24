/*
Copyright 2019 The Kubernetes Authors.

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

package driver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	nodeCaps   = []csi.NodeServiceCapability_RPC_Type{}
	sourcePath string
)

func (d *Driver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	context := req.GetVolumeContext()
	mdsIPAddress := context[volumeContextMDSIPAddress]
	lustreFSName := context[volumeContextFSName]

	sourcePath := fmt.Sprintf("%s@tcp:/%s", mdsIPAddress, lustreFSName)

	output, mountError := execShellCmd("findmnt")
	fmt.Println("Output: \"", output, "\"")
	if mountError != nil {
		fmt.Println("Can't tell if Lustre is already mounted (NodePublishVolume): \"", mountError, "\"")
		return nil, status.Error(codes.Internal, fmt.Sprintf("Can't tell if Lustre is already mounted (NodePublishVolume) \"%s\"", mountError))
	}

	if strings.Contains(output, sourcePath) {
		fmt.Println("Lustre already mounted (NodePublishVolume)")
		fmt.Println("NodePublishVolume successful.")
		return &csi.NodePublishVolumeResponse{}, nil
	}

	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		fmt.Println("Target path not provided")
		return nil, status.Error(codes.InvalidArgument, "Target path not provided")
	}

	err := os.MkdirAll(targetPath, os.FileMode(0775))
	if err != nil {
		fmt.Println("Could not create dir %q: %v", targetPath, err)
		return nil, status.Errorf(codes.Internal, "Could not create dir %q: %v", targetPath, err)
	}

	output, errmount := execShellCmd("mount -t lustre %s %s", sourcePath, targetPath)
	if errmount != nil {
		fmt.Println("Could not mount %q at %q: %v, reason %s", sourcePath, targetPath,
			errmount, output)
		return nil, status.Errorf(codes.Internal, "Could not mount %q at %q: %v, reason %s", sourcePath, targetPath,
			errmount, output)
	}

	fmt.Println("NodePublishVolume successful.")
	return &csi.NodePublishVolumeResponse{}, nil
}

func execShellCmd(format string, args ...interface{}) (string, error) {
	cmd := fmt.Sprintf(format, args...)
	fmt.Println("execShellCmd: ", cmd)
	shCmd := exec.Command("/bin/sh", "-c", cmd)
	output, err := shCmd.CombinedOutput()
	if err != nil {
		return string(output), err
	}
	return string(output), nil
}

func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {

	output, mountError := execShellCmd("findmnt")
	if mountError != nil {
		return nil, status.Error(codes.Internal, "Can't tell if Lustre is already mounted (NodeUnpublishVolume)")
	}
	if !strings.Contains(output, sourcePath) {
		fmt.Println("Lustre not currently mounted (NodeUnpublishVolume)")
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}

	fmt.Println("Lustre is mounted and we're unpublishing.  Unmounting...")
	targetPath := req.GetTargetPath()
	output, unmounterr := execShellCmd("umount %s", targetPath)
	if unmounterr != nil && !strings.Contains(output, "not mounted") {
		fmt.Println("Could not unmount %s: %v, reason %s", targetPath,
			unmounterr, output)
		return nil, status.Errorf(codes.Internal, "Could not unmount %q: %v, reason %s", targetPath,
			unmounterr, output)
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (d *Driver) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	var caps []*csi.NodeServiceCapability
	for _, cap := range nodeCaps {
		c := &csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: cap,
				},
			},
		}
		caps = append(caps, c)
	}
	return &csi.NodeGetCapabilitiesResponse{Capabilities: caps}, nil
}

func (d *Driver) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	hostname, _ := execShellCmd("hostname | xargs echo -n")
	return &csi.NodeGetInfoResponse{
		NodeId: hostname,
	}, nil
}
