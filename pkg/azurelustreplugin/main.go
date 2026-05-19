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

package main

import (
	"errors"
	"flag"
	"fmt"

	"k8s.io/klog/v2"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/azurelustre"
)

var (
	endpoint                     = flag.String("endpoint", "unix://tmp/csi.sock", "CSI endpoint")
	nodeID                       = flag.String("nodeid", "", "node id")
	version                      = flag.Bool("version", false, "Print the version and exit.")
	driverName                   = flag.String("drivername", azurelustre.DefaultDriverName, "name of the driver")
	enableAzureLustreMockMount   = flag.Bool("enable-azurelustre-mock-mount", false, "Whether enable mock mount(only for testing)")
	enableAzureLustreMockDynProv = flag.Bool("enable-azurelustre-mock-dyn-prov", true, "Whether enable mock dynamic provisioning(only for testing)")
	workingMountDir              = flag.String("working-mount-dir", "/tmp", "working directory for provisioner to mount lustre filesystems temporarily")
	removeNotReadyTaint          = flag.Bool("remove-not-ready-taint", true, "remove NotReady taint from node when node is ready")

	errDriverInitFailed       = errors.New("failed to initialize Azure Lustre CSI driver")
	errDriverRunReturnedEarly = errors.New("driver.Run returned unexpectedly")
)

func main() {
	if err := run(); err != nil {
		klog.Fatalln(err)
	}
}

func run() error {
	if err := initKlogFlags(flag.CommandLine); err != nil {
		return fmt.Errorf("failed to initialize klog flags: %w", err)
	}
	flag.Parse()
	if *version {
		info, err := azurelustre.GetVersionYAML(*driverName)
		if err != nil {
			return fmt.Errorf("failed to get version: %w", err)
		}
		klog.V(2).Info(info)
		_, err = fmt.Println(info) //nolint:forbidigo // Print version info to stdout for access through kubectl exec
		if err != nil {
			return fmt.Errorf("failed to print version: %w", err)
		}
		return nil
	}

	return handle()
}

func handle() error {
	driverOptions := azurelustre.DriverOptions{
		NodeID:                       *nodeID,
		DriverName:                   *driverName,
		EnableAzureLustreMockMount:   *enableAzureLustreMockMount,
		EnableAzureLustreMockDynProv: *enableAzureLustreMockDynProv,
		WorkingMountDir:              *workingMountDir,
		RemoveNotReadyTaint:          *removeNotReadyTaint,
	}
	driver := azurelustre.NewDriver(&driverOptions)
	if driver == nil {
		return errDriverInitFailed
	}
	driver.Run(*endpoint, false)
	// driver.Run is expected to block forever serving the CSI gRPC endpoint;
	// returning means the server stopped without an explicit shutdown signal,
	// which should surface as a non-zero process exit.
	return errDriverRunReturnedEarly
}

// initKlogFlags registers klog flags on the provided FlagSet and configures
// defaults for the CSI driver:
//   - logtostderr=true: log to stderr instead of files
//   - legacy_stderr_threshold_behavior=false: honor stderrthreshold even when logtostderr=true
//   - stderrthreshold=INFO: default to all severity levels (overridable via --stderrthreshold)
func initKlogFlags(fs *flag.FlagSet) error {
	klog.InitFlags(fs)
	return errors.Join(
		fs.Set("logtostderr", "true"),
		fs.Set("legacy_stderr_threshold_behavior", "false"),
		fs.Set("stderrthreshold", "INFO"),
	)
}
