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

package amlfs

// TODO_JUSJIN: update references to blob-csi-driver packages as needed

import (
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"

	csicommon "sigs.k8s.io/blob-csi-driver/pkg/csi-common"
	"sigs.k8s.io/blob-csi-driver/pkg/util"
)

const (
	// DefaultDriverName holds the name of the csi-driver
	DefaultDriverName = "amlfs.csi.azure.com"
	blobCSIDriverName = "amlfs_csi_driver"
	separator         = "#"
	volumeIDTemplate  = "%s#%s#%s"
	ephemeralField    = "csi.storage.k8s.io/ephemeral"
	podNamespaceField = "csi.storage.k8s.io/pod.namespace"
	mountOptionsField = "mountoptions"
	falseValue        = "false"
	trueValue         = "true"

	pvcNameKey      = "csi.storage.k8s.io/pvc/name"
	pvcNamespaceKey = "csi.storage.k8s.io/pvc/namespace"
	pvNameKey       = "csi.storage.k8s.io/pv/name"
)

var (
	retriableErrors = []string{}
)

// DriverOptions defines driver parameters specified in driver deployment
type DriverOptions struct {
	NodeID              string
	DriverName          string
	EnableBlobMockMount bool
}

// Driver implements all interfaces of CSI drivers
type Driver struct {
	csicommon.CSIDriver
	csicommon.DefaultIdentityServer
	csicommon.DefaultControllerServer
	csicommon.DefaultNodeServer
	// enableBlobMockMount is only for testing, DO NOT set as true in non-testing scenario
	enableBlobMockMount bool
	mounter             *mount.SafeFormatAndMount // TODO_JUSJIN: check any other alternatives
	volLockMap          *util.LockMap
	// A map storing all volumes with ongoing operations so that additional operations
	// for that same volume (as defined by VolumeID) return an Aborted error
	volumeLocks *volumeLocks
}

// NewDriver Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func NewDriver(options *DriverOptions) *Driver {
	d := Driver{
		volLockMap:          util.NewLockMap(),
		volumeLocks:         newVolumeLocks(),
		enableBlobMockMount: options.EnableBlobMockMount,
	}
	d.Name = options.DriverName
	d.Version = driverVersion
	d.NodeID = options.NodeID

	d.DefaultControllerServer.Driver = &d.CSIDriver
	d.DefaultIdentityServer.Driver = &d.CSIDriver
	d.DefaultNodeServer.Driver = &d.CSIDriver

	return &d
}

// Run driver initialization
func (d *Driver) Run(endpoint string, testBool bool) {
	versionMeta, err := GetVersionYAML(d.Name)
	if err != nil {
		klog.Fatalf("%v", err)
	}
	klog.Infof("\nDRIVER INFORMATION:\n-------------------\n%s\n\nStreaming logs below:", versionMeta)

	d.mounter = &mount.SafeFormatAndMount{
		Interface: mount.New(""),
		Exec:      utilexec.New(),
	}

	// TODO_JUSJIN: revist these caps
	// Initialize default library driver
	d.AddControllerServiceCapabilities(
		[]csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
			csi.ControllerServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
		})
	d.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
	})

	d.AddNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		csi.NodeServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
	})

	s := csicommon.NewNonBlockingGRPCServer()
	// Driver d act as IdentityServer, ControllerServer and NodeServer
	s.Start(endpoint, d, d, d, testBool)
	s.Wait()
}

func IsCorruptedDir(dir string) bool {
	_, pathErr := mount.PathExists(dir)
	return pathErr != nil && mount.IsCorruptedMnt(pathErr)
}

func isRetriableError(err error) bool {
	if err != nil {
		for _, v := range retriableErrors {
			if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(v)) {
				return true
			}
		}
	}
	return false
}
