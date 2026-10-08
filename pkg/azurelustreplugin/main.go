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
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/klog/v2"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/azurelustre"
)

var (
	endpoint                     = flag.String("endpoint", "unix://tmp/csi.sock", "CSI endpoint")
	nodeID                       = flag.String("nodeid", "", "node id")
	version                      = flag.Bool("version", false, "Print the version and exit.")
	driverName                   = flag.String("drivername", azurelustre.DefaultDriverName, "name of the driver")
	preDeleteCheck               = flag.Bool("pre-delete-check", false, "Check for driver-owned PersistentVolumes and exit")
	preDeleteCheckTimeout        = flag.Duration("pre-delete-check-timeout", 30*time.Second, "Timeout for the pre-delete PersistentVolume check")
	enableAzureLustreMockMount   = flag.Bool("enable-azurelustre-mock-mount", false, "Whether enable mock mount(only for testing)")
	enableAzureLustreMockDynProv = flag.Bool("enable-azurelustre-mock-dyn-prov", false, "Whether enable mock dynamic provisioning(only for testing)")
	allowUnadvertisedZones       = flag.Bool("allow-unadvertised-zones", false, "Allow an explicitly specified zone when SKU metadata advertises none")
	workingMountDir              = flag.String("working-mount-dir", "/tmp", "working directory for provisioner to mount lustre filesystems temporarily")
	removeNotReadyTaint          = flag.Bool("remove-not-ready-taint", true, "remove NotReady taint from node when node is ready")
	preDeleteCheckFunc           = azurelustre.CheckPersistentVolumesBeforeUninstall

	errDriverInitFailed       = errors.New("failed to initialize Azure Lustre CSI driver")
	errDriverRunReturnedEarly = errors.New("driver.Run returned unexpectedly")
)

func main() {
	if err := run(); err != nil {
		klog.Fatalln(err) //nolint:forbidigo // Only the executable entry point terminates the process.
	}
}

func run() error {
	if err := initKlogFlags(flag.CommandLine); err != nil {
		return fmt.Errorf("failed to initialize klog flags: %w", err)
	}
	flag.Parse()
	if *preDeleteCheck {
		return runPreDeleteCheck(*preDeleteCheckTimeout, *driverName, preDeleteCheckFunc)
	}
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return handle(ctx)
}

func runPreDeleteCheck(
	timeout time.Duration,
	driverName string,
	check func(context.Context, string) error,
) error {
	if timeout <= 0 {
		return errors.New("pre-delete check timeout must be greater than zero")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := check(ctx, driverName); err != nil {
		return fmt.Errorf("pre-delete check failed: %w", err)
	}

	klog.Infof("No PersistentVolumes use CSI driver %q; allowing uninstall", driverName)
	return nil
}

func handle(ctx context.Context) error {
	driverOptions := newDriverOptions()
	driver, err := azurelustre.NewDriver(ctx, &driverOptions)
	if err != nil {
		return errors.Join(errDriverInitFailed, err)
	}
	if err := driver.Run(ctx, *endpoint); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	default:
		return errDriverRunReturnedEarly
	}
}

func newDriverOptions() azurelustre.DriverOptions {
	return azurelustre.DriverOptions{
		NodeID:                       *nodeID,
		DriverName:                   *driverName,
		EnableAzureLustreMockMount:   *enableAzureLustreMockMount,
		EnableAzureLustreMockDynProv: *enableAzureLustreMockDynProv,
		AllowUnadvertisedZones:       *allowUnadvertisedZones,
		WorkingMountDir:              *workingMountDir,
		RemoveNotReadyTaint:          *removeNotReadyTaint,
	}
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
