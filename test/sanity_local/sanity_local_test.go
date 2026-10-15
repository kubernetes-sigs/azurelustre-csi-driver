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

package sanitylocal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/azurelustre"
)

func TestSanity(t *testing.T) {
	testDir := t.TempDir()
	socketEndpoint := filepath.Join(testDir, "csi.sock")
	targetPath := filepath.Join(testDir, "targetPath")
	stagingPath := filepath.Join(testDir, "stagingPath")
	socketEndpoint = "unix://" + socketEndpoint
	config := sanity.NewTestConfig()
	config.Address = socketEndpoint
	config.TargetPath = targetPath
	config.StagingPath = stagingPath
	config.TestVolumeParameters = map[string]string{
		azurelustre.VolumeContextMGSIPAddress: "127.0.0.1",
	}
	driverOptions := azurelustre.DriverOptions{
		NodeID:                       "fakeNodeID",
		DriverName:                   "fake",
		EnableAzureLustreMockMount:   true,
		EnableAzureLustreMockDynProv: true,
	}
	driver, err := azurelustre.NewDriver(t.Context(), &driverOptions)
	if err != nil {
		t.Fatalf("failed to create driver: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		if err := <-finished; err != nil {
			t.Errorf("driver.Run returned error: %v", err)
		}
	})
	go func() {
		finished <- driver.Run(ctx, socketEndpoint)
	}()
	sanity.Test(t, config)
}
