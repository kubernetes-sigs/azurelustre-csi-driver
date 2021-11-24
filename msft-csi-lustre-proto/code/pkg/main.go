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
package main

import (
	"flag"
	"fmt"
	"msft-laaso-lustre-csi/pkg/driver"
	"net"
	"os"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
)

const (
	csiVersion = "0.0.1"
	driverName = "msft.laaso.lustre.com"
)

func main() {
	var (
		endpoint = os.Getenv("CSI_ENDPOINT")
	)

	fmt.Println("Arguments to main method of CSI: ", os.Args)
	flag.Parse()
	fmt.Println("Removing endpoint path: ", endpoint)
	os.Remove(endpoint)
	var epFolder = filepath.Dir(endpoint)
	fmt.Println("Creating endpoint folder: ", epFolder)
	os.MkdirAll(epFolder, 0755)
	fmt.Println("Entering the main method with EP: ", endpoint)
	listener, _ := net.Listen("unix", endpoint)

	fmt.Println("net.Listen started")
	d := driver.GetDriver(driverName, csiVersion)
	fmt.Println("driver.GetDriver returned name=", driverName, ", version=", csiVersion)
	server := grpc.NewServer()
	fmt.Println("grpc.NewServer started")

	csi.RegisterIdentityServer(server, d)
	fmt.Println("IdentityServer registered")
	csi.RegisterControllerServer(server, d)
	fmt.Println("ControllerServer registered")
	csi.RegisterNodeServer(server, d)
	fmt.Println("NodeServer registered")

	server.Serve(listener)
	fmt.Println("server running")
}
