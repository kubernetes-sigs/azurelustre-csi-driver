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
	"context"
	"errors"
	"flag"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
