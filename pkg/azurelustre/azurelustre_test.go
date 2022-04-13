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

package azurelustre

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TODO_JUSJIN: update and add tests

const (
	fakeNodeID     = "fakeNodeID"
	fakeDriverName = "fake"
	vendorVersion  = "0.3.0"
)

func NewFakeDriver() *Driver {
	driverOptions := DriverOptions{
		NodeID:               fakeNodeID,
		DriverName:           DefaultDriverName,
		EnableAzureLustreMockMount: false,
	}
	driver := NewDriver(&driverOptions)
	driver.Name = fakeDriverName
	driver.Version = vendorVersion
	return driver
}

func TestNewFakeDriver(t *testing.T) {
	driverOptions := DriverOptions{
		NodeID:               fakeNodeID,
		DriverName:           DefaultDriverName,
		EnableAzureLustreMockMount: false,
	}
	d := NewDriver(&driverOptions)
	assert.NotNil(t, d)
}

func TestNewDriver(t *testing.T) {
	driverOptions := DriverOptions{
		NodeID:               fakeNodeID,
		DriverName:           DefaultDriverName,
		EnableAzureLustreMockMount: false,
	}
	driver := NewDriver(&driverOptions)
	fakedriver := NewFakeDriver()
	fakedriver.Name = DefaultDriverName
	fakedriver.Version = driverVersion
	assert.Equal(t, driver, fakedriver)
}

func TestIsRetriableError(t *testing.T) {
	tests := []struct {
		desc         string
		rpcErr       error
		expectedBool bool
	}{
		{
			desc:         "non-retriable error",
			rpcErr:       nil,
			expectedBool: false,
		},
	}

	for _, test := range tests {
		result := isRetriableError(test.rpcErr)
		if result != test.expectedBool {
			t.Errorf("desc: (%s), input: rpcErr(%v), isRetriableError returned with bool(%v), not equal to expectedBool(%v)",
				test.desc, test.rpcErr, result, test.expectedBool)
		}
	}
}

func TestIsCorruptedDir(t *testing.T) {
	existingMountPath, err := ioutil.TempDir(os.TempDir(), "azurelustre-csi-mount-test")
	if err != nil {
		t.Fatalf("failed to create tmp dir: %v", err)
	}
	defer os.RemoveAll(existingMountPath)

	tests := []struct {
		desc           string
		dir            string
		expectedResult bool
	}{
		{
			desc:           "NotExist dir",
			dir:            "/tmp/NotExist",
			expectedResult: false,
		},
		{
			desc:           "Existing dir",
			dir:            existingMountPath,
			expectedResult: false,
		},
	}

	for i, test := range tests {
		isCorruptedDir := IsCorruptedDir(test.dir)
		assert.Equal(t, test.expectedResult, isCorruptedDir, "TestCase[%d]: %s", i, test.desc)
	}
}
