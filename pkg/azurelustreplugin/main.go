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

	"sigs.k8s.io/azurelustre-csi-driver/pkg/azurelustre"

	"k8s.io/klog/v2"
)

func init() {
	_ = flag.Set("logtostderr", "true")
}

var (
	endpoint                   = flag.String("endpoint", "unix://tmp/csi.sock", "CSI endpoint")
	nodeID                     = flag.String("nodeid", "", "node id")
	version                    = flag.Bool("version", false, "Print the version and exit.")
	driverName                 = flag.String("drivername", azurelustre.DefaultDriverName, "name of the driver")
	enableAzureLustreMockMount = flag.Bool("enable-azurelustre-mock-mount", false, "Whether enable mock mount(only for testing)")
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()
	if *version {
		info, err := azurelustre.GetVersionYAML(*driverName)
		if err != nil {
			klog.Fatalln(err)
		}
		fmt.Println(info)
		os.Exit(0)
	}

	handle()
	os.Exit(0)
}

func handle() {
	driverOptions := azurelustre.DriverOptions{
		NodeID:                     *nodeID,
		DriverName:                 *driverName,
		EnableAzureLustreMockMount: *enableAzureLustreMockMount,
	}
	driver := azurelustre.NewDriver(&driverOptions)
	if driver == nil {
		klog.Fatalln("Failed to initialize Azure Lustre CSI driver")
	}
	driver.Run(*endpoint, false)
}
