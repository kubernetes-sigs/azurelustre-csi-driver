/*
Copyright 2025 The Kubernetes Authors.

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
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/azurelustre"
)

func TestInitKlogFlags(t *testing.T) {
	// Arrange
	fs := flag.NewFlagSet("test", flag.ContinueOnError)

	// Act
	err := initKlogFlags(fs)

	// Assert
	require.NoError(t, err)

	expectedFlags := map[string]string{
		"logtostderr":                      "true",
		"legacy_stderr_threshold_behavior": "false",
		// klog's severityValue.String() returns the numeric value; INFO = 0
		"stderrthreshold": "0",
	}

	for name, want := range expectedFlags {
		f := fs.Lookup(name)
		require.NotNilf(t, f, "flag %q not found", name)
		assert.Equalf(t, want, f.Value.String(), "flag %q", name)
	}
}

func TestInitKlogFlags_UserOverride(t *testing.T) {
	// Arrange
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	err := initKlogFlags(fs)
	require.NoError(t, err)

	// Act — simulate user passing --stderrthreshold=WARNING on the command line
	err = fs.Parse([]string{"--stderrthreshold=WARNING"})

	// Assert
	require.NoError(t, err)
	f := fs.Lookup("stderrthreshold")
	require.NotNilf(t, f, "flag %q not found", "stderrthreshold")
	// WARNING = severity 1
	assert.Equalf(t, "1", f.Value.String(), "flag %q after user override", "stderrthreshold")
}

func TestInitKlogFlags_InvalidThreshold(t *testing.T) {
	// Arrange
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	err := initKlogFlags(fs)
	require.NoError(t, err)

	// Act — simulate user passing an invalid threshold
	err = fs.Parse([]string{"--stderrthreshold=GARBAGE"})

	// Assert
	assert.Error(t, err, "expected error for invalid stderrthreshold value")
}

func TestNewDriverOptions_AllowUnadvertisedZones(t *testing.T) {
	original := *allowUnadvertisedZones
	t.Cleanup(func() {
		*allowUnadvertisedZones = original
	})
	*allowUnadvertisedZones = true

	options := newDriverOptions()

	assert.True(t, options.AllowUnadvertisedZones)
}

func TestRunPreDeleteCheck(t *testing.T) {
	// Arrange
	called := false
	actualDriverName := ""
	hasDeadline := false
	check := func(ctx context.Context, driverName string) error {
		called = true
		actualDriverName = driverName
		_, hasDeadline = ctx.Deadline()
		return nil
	}

	// Act
	err := runPreDeleteCheck(time.Second, "test.csi.example.com", check)

	// Assert
	require.NoError(t, err)
	assert.True(t, called, "pre-delete check callback was not invoked")
	assert.Equal(t, "test.csi.example.com", actualDriverName)
	assert.True(t, hasDeadline, "pre-delete check context did not have a deadline")
}

func TestRunDispatchesPreDeleteCheck(t *testing.T) {
	// Arrange
	originalFlags := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet(originalFlags.Name(), flag.ContinueOnError)
	originalFlags.VisitAll(func(f *flag.Flag) {
		flag.CommandLine.Var(f.Value, f.Name, f.Usage)
	})
	originalEnabled := *preDeleteCheck
	originalTimeout := *preDeleteCheckTimeout
	originalDriverName := *driverName
	originalCheck := preDeleteCheckFunc
	t.Cleanup(func() {
		flag.CommandLine = originalFlags
		*preDeleteCheck = originalEnabled
		*preDeleteCheckTimeout = originalTimeout
		*driverName = originalDriverName
		preDeleteCheckFunc = originalCheck
	})

	*preDeleteCheck = true
	*preDeleteCheckTimeout = time.Second
	*driverName = "test.csi.example.com"
	called := false
	preDeleteCheckFunc = func(context.Context, string) error {
		called = true
		return nil
	}

	// Act
	err := run()

	// Assert
	require.NoError(t, err)
	assert.True(t, called, "run did not dispatch the pre-delete check")
}

func TestRunPreDeleteCheckRejectsInvalidTimeout(t *testing.T) {
	// Arrange
	called := false
	check := func(context.Context, string) error {
		called = true
		return nil
	}

	// Act
	err := runPreDeleteCheck(0, "test.csi.example.com", check)

	// Assert
	require.EqualError(t, err, "pre-delete check timeout must be greater than zero")
	assert.False(t, called, "pre-delete check callback ran with an invalid timeout")
}

func TestRunPreDeleteCheckReturnsCheckError(t *testing.T) {
	// Arrange
	expectedErr := errors.New("list failed")
	check := func(context.Context, string) error {
		return expectedErr
	}

	// Act
	err := runPreDeleteCheck(time.Second, "test.csi.example.com", check)

	// Assert
	require.Error(t, err)
	require.ErrorIs(t, err, expectedErr)
	assert.EqualError(t, err, "pre-delete check failed: list failed")
}

func TestRunPreDeleteCheckDeadline(t *testing.T) {
	check := func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}

	err := runPreDeleteCheck(time.Millisecond, "test.csi.example.com", check)

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestHandleFailure(t *testing.T) {
	t.Setenv(azurelustre.DefaultAzureConfigFileEnv, filepath.Join(t.TempDir(), "missing.json"))
	originalMock := *enableAzureLustreMockDynProv
	originalEndpoint := *endpoint
	t.Cleanup(func() {
		*enableAzureLustreMockDynProv = originalMock
		*endpoint = originalEndpoint
	})
	*enableAzureLustreMockDynProv = false
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := handle(ctx)
	require.ErrorIs(t, err, errDriverInitFailed)
	*enableAzureLustreMockDynProv = true
	*endpoint = "invalid"
	err = handle(t.Context())
	require.ErrorContains(t, err, "invalid endpoint")
}

func TestRunSignalProcess(t *testing.T) {
	if os.Getenv("CSI_LIFECYCLE_CHILD") != "1" {
		return
	}
	os.Args = []string{
		os.Args[0],
		"--endpoint=" + os.Getenv("CSI_LIFECYCLE_ENDPOINT"),
		"--enable-azurelustre-mock-dyn-prov=true",
		"--enable-azurelustre-mock-mount=true",
	}
	err := run()
	require.NoError(t, err)
}

func TestRunSignals(t *testing.T) {
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			testRunSignal(t, signal)
		})
	}
}

func testRunSignal(t *testing.T, signal os.Signal) {
	t.Helper()
	testDir := t.TempDir()
	t.Setenv(azurelustre.DefaultAzureConfigFileEnv, filepath.Join(testDir, "missing.json"))
	t.Setenv("CSI_LIFECYCLE_CHILD", "1")
	t.Setenv("CSI_LIFECYCLE_ENDPOINT", "unix://"+filepath.Join(testDir, "csi.sock"))
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestRunSignalProcess$") //nolint:gosec // Runs only the current test binary from os.Executable.
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err = command.Start()
	require.NoError(t, err)
	finished := make(chan struct{})
	var waitErr error
	go func() {
		defer close(finished)
		waitErr = command.Wait()
	}()
	t.Cleanup(func() {
		cancel()
		<-finished
		assert.NoError(t, waitErr, "%s", output.String())
	})
	connection, err := grpc.NewClient(os.Getenv("CSI_LIFECYCLE_ENDPOINT"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() {
		err := connection.Close()
		require.NoError(t, err)
	})
	_, err = csi.NewIdentityClient(connection).Probe(ctx, &csi.ProbeRequest{}, grpc.WaitForReady(true))
	require.NoError(t, err)
	err = command.Process.Signal(signal)
	require.NoError(t, err)
	<-finished
	require.NoError(t, waitErr, "%s", output.String())
}
