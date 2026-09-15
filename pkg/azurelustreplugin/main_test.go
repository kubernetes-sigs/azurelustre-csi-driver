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
	"flag"
	"testing"

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
