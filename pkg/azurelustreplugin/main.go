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
	"flag"
	"fmt"
	"os"

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
)

func main() {
	klog.InitFlags(nil)
	err := flag.Set("logtostderr", "true")
	if err != nil {
		klog.Fatalln(err)
	}
	flag.Parse()
	if *version {
		info, err := azurelustre.GetVersionYAML(*driverName)
		if err != nil {
			klog.Fatalln(err)
		}
		klog.V(2).Info(info)
		fmt.Println(info) //nolint:forbidigo // Print version info to stdout for access through kubectl exec
		os.Exit(0)
	}

	handle()
	os.Exit(0)
}

func handle() {
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
		klog.Fatalln("Failed to initialize Azure Lustre CSI driver")
	}
	driver.Run(*endpoint, false)
}
