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
	"errors"
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDriverRunError(t *testing.T) {
	failure := errors.New("controller lifecycle failure")
	for _, test := range []struct {
		name               string
		runErr             error
		isStatusController bool
		want               error
	}{
		{"graceful status-controller shutdown", nil, true, nil},
		{"status-controller failure", failure, true, failure},
		{"unexpected CSI server exit", nil, false, errDriverRunReturnedEarly},
		{"CSI server failure", failure, false, failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := driverRunError(test.runErr, test.isStatusController)
			if test.want == nil {
				require.NoError(t, got)
			} else {
				require.ErrorIs(t, got, test.want)
			}
		})
	}
}

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

func TestNewDriverOptions_StatusControllerOnly(t *testing.T) {
	originalStatusControllerOnly := *statusControllerOnly
	originalAllowUnadvertisedZones := *allowUnadvertisedZones
	t.Cleanup(func() {
		*statusControllerOnly = originalStatusControllerOnly
		*allowUnadvertisedZones = originalAllowUnadvertisedZones
	})
	*allowUnadvertisedZones = true

	for _, test := range []struct {
		name    string
		enabled bool
	}{
		{"disabled", false},
		{"enabled", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			*statusControllerOnly = test.enabled

			options := newDriverOptions()

			assert.Equal(t, test.enabled, options.StatusControllerOnly)
			assert.True(t, options.AllowUnadvertisedZones)
		})
	}
}
