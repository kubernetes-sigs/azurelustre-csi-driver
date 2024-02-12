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
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const (
	fakeNodeID                = "fakeNodeID"
	fakeDriverName            = "fake"
	vendorVersion             = "0.3.0"
	clusterRequestFailureName = "testShouldFail"
)

func NewFakeDriver() *Driver {
	driverOptions := DriverOptions{
		NodeID:                       fakeNodeID,
		DriverName:                   DefaultDriverName,
		EnableAzureLustreMockMount:   false,
		EnableAzureLustreMockDynProv: true,
	}
	driver := NewDriver(&driverOptions)
	driver.Name = fakeDriverName
	driver.Version = vendorVersion
	driver.cloud = &azure.Cloud{}
	driver.cloud.SubscriptionID = "defaultFakeSubID"
	driver.location = "defaultFakeLocation"
	driver.resourceGroup = "defaultFakeResourceGroup"
	driver.dynamicProvisioner = &FakeDynamicProvisioner{}

	return driver
}

type FakeDynamicProvisioner struct {
	DynamicProvisionerInterface
	Filesystems   []*AmlFilesystemProperties
	fakeCallCount map[string]int
}

func (f *FakeDynamicProvisioner) recordFakeCall(name string) {
	if f.fakeCallCount == nil {
		f.fakeCallCount = make(map[string]int)
	}
	f.fakeCallCount[name]++
}

func (f *FakeDynamicProvisioner) CreateAmlFilesystem(_ context.Context, amlFilesystemProperties *AmlFilesystemProperties) (string, error) {
	f.recordFakeCall("CreateAmlFilesystem")
	if strings.HasSuffix(amlFilesystemProperties.AmlFilesystemName, clusterRequestFailureName) {
		return "", status.Errorf(codes.InvalidArgument, "error occurred calling API: %s", clusterRequestFailureName)
	}
	f.Filesystems = append(f.Filesystems, amlFilesystemProperties)
	return "127.0.0.2", nil
}

func (f *FakeDynamicProvisioner) DeleteAmlFilesystem(_ context.Context, _, amlFilesystemName string) error {
	f.recordFakeCall("DeleteAmlFilesystem")
	if amlFilesystemName == clusterRequestFailureName {
		return status.Errorf(codes.InvalidArgument, "error occurred calling API: %s", clusterRequestFailureName)
	}
	f.Filesystems = slices.DeleteFunc(f.Filesystems, func(filesystem *AmlFilesystemProperties) bool {
		return filesystem.AmlFilesystemName == amlFilesystemName
	})
	return nil
}

func (f *FakeDynamicProvisioner) GetSkuValuesForLocation(_ context.Context, _ string) map[string]*LustreSkuValue {
	f.recordFakeCall("GetSkuValuesForLocation")
	return map[string]*LustreSkuValue{
		"AMLFS-Durable-Premium-40":  {IncrementInTib: 48, MaximumInTib: 768},
		"AMLFS-Durable-Premium-125": {IncrementInTib: 16, MaximumInTib: 128},
		"AMLFS-Durable-Premium-250": {IncrementInTib: 8, MaximumInTib: 128},
		"AMLFS-Durable-Premium-500": {IncrementInTib: 4, MaximumInTib: 128},
	}
}

func TestNewDriver(t *testing.T) {
	driverOptions := DriverOptions{
		NodeID:                       fakeNodeID,
		DriverName:                   DefaultDriverName,
		EnableAzureLustreMockMount:   false,
		EnableAzureLustreMockDynProv: true,
	}
	d := NewDriver(&driverOptions)
	assert.NotNil(t, d)
}

func TestIsCorruptedDir(t *testing.T) {
	existingMountPath, err := os.MkdirTemp(os.TempDir(), "azurelustre-csi-mount-test")
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

func TestGetLustreVolFromID(t *testing.T) {
	cases := []struct {
		desc                 string
		volumeID             string
		expectedLustreVolume *lustreVolume
		expectedErr          error
	}{
		{
			desc:     "correct old volume id",
			volumeID: "vol_1#lustrefs#1.1.1.1",
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "",
			},
		},
		{
			desc:     "correct simple volume id",
			volumeID: "vol_1#lustrefs#1.1.1.1##",
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1##",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "",
			},
		},
		{
			desc:     "correct volume id",
			volumeID: "vol_1#lustrefs#1.1.1.1#testSubDir#t#testAmlfsRg",
			expectedLustreVolume: &lustreVolume{
				id:                           "vol_1#lustrefs#1.1.1.1#testSubDir#t#testAmlfsRg",
				name:                         "vol_1",
				azureLustreName:              "lustrefs",
				mgsIPAddress:                 "1.1.1.1",
				subDir:                       "testSubDir",
				createdByDynamicProvisioning: true,
				resourceGroupName:            "testAmlfsRg",
			},
		},
		{
			desc:     "correct volume id with extra slashes",
			volumeID: "vol_1#lustrefs/#1.1.1.1#/testSubDir/",
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs/#1.1.1.1#/testSubDir/",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir",
			},
		},
		{
			desc:     "correct volume id with empty sub-dir",
			volumeID: "vol_1#lustrefs#1.1.1.1##",
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1##",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "",
			},
		},
		{
			desc:     "correct volume id with empty sub-dir, old format",
			volumeID: "vol_1#lustrefs#1.1.1.1#",
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "",
			},
		},
		{
			desc:     "correct volume id with filesystem rg but empty sub-dir",
			volumeID: "vol_1#lustrefs#1.1.1.1##t#testAmlfsRg",
			expectedLustreVolume: &lustreVolume{
				id:                           "vol_1#lustrefs#1.1.1.1##t#testAmlfsRg",
				name:                         "vol_1",
				azureLustreName:              "lustrefs",
				mgsIPAddress:                 "1.1.1.1",
				subDir:                       "",
				createdByDynamicProvisioning: true,
				resourceGroupName:            "testAmlfsRg",
			},
		},
		{
			desc:     "correct volume id with multiple sub-dir levels",
			volumeID: "vol_1#lustrefs#1.1.1.1#testSubDir/nestedSubDir",
			expectedLustreVolume: &lustreVolume{
				id:              "vol_1#lustrefs#1.1.1.1#testSubDir/nestedSubDir",
				name:            "vol_1",
				azureLustreName: "lustrefs",
				mgsIPAddress:    "1.1.1.1",
				subDir:          "testSubDir/nestedSubDir",
			},
		},
		{
			desc:                 "incorrect volume id",
			volumeID:             "vol_1",
			expectedLustreVolume: nil,
			expectedErr:          errors.New("could not split volume ID \"vol_1\" into lustre name and ip address"),
		},
	}
	for _, test := range cases {
		t.Run(test.desc, func(t *testing.T) {
			lustreVolume, err := getLustreVolFromID(test.volumeID)

			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("Desc: %v, Expected error: %v, Actual error: %v", test.desc, test.expectedErr, err)
			}
			assert.Equal(t, test.expectedLustreVolume, lustreVolume, "Desc: %s - Incorrect lustre volume: %v - Expected: %v", test.desc, lustreVolume, test.expectedLustreVolume)
		})
	}
}

func TestPopulateSubnetPropertiesFromCloudConfig(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "NetworkResourceSubscriptionID is Empty",
			testFunc: func(t *testing.T) {
				d := NewFakeDriver()
				d.cloud = &azure.Cloud{}
				d.cloud.SubscriptionID = "fakeSubID"
				d.cloud.NetworkResourceSubscriptionID = ""
				d.cloud.ResourceGroup = "foo"
				d.cloud.VnetResourceGroup = "foo"
				actualOutput := d.populateSubnetPropertiesFromCloudConfig(SubnetProperties{
					VnetResourceGroup: "",
					VnetName:          "",
					SubnetName:        "",
				})
				expectedSubnetID := fmt.Sprintf(subnetTemplate, d.cloud.SubscriptionID, "foo", d.cloud.VnetName, d.cloud.SubnetName)
				expectedOutput := SubnetProperties{
					VnetResourceGroup: "foo",
					VnetName:          d.cloud.VnetName,
					SubnetName:        d.cloud.SubnetName,
					SubnetID:          expectedSubnetID,
				}
				assert.Equal(t, expectedOutput, actualOutput, "cloud.SubscriptionID should be used as the SubID")
			},
		},
		{
			name: "NetworkResourceSubscriptionID is not Empty",
			testFunc: func(t *testing.T) {
				d := NewFakeDriver()
				d.cloud = &azure.Cloud{}
				d.cloud.SubscriptionID = "fakeSubID"
				d.cloud.NetworkResourceSubscriptionID = "fakeNetSubID"
				d.cloud.ResourceGroup = "foo"
				d.cloud.VnetResourceGroup = "foo"
				actualOutput := d.populateSubnetPropertiesFromCloudConfig(SubnetProperties{
					VnetResourceGroup: "",
					VnetName:          "",
					SubnetName:        "",
				})
				expectedSubnetID := fmt.Sprintf(subnetTemplate, d.cloud.NetworkResourceSubscriptionID, "foo", d.cloud.VnetName, d.cloud.SubnetName)
				expectedOutput := SubnetProperties{
					VnetResourceGroup: "foo",
					VnetName:          d.cloud.VnetName,
					SubnetName:        d.cloud.SubnetName,
					SubnetID:          expectedSubnetID,
				}
				assert.Equal(t, expectedOutput, actualOutput, "cloud.NetworkResourceSubscriptionID should be used as the SubID")
			},
		},
		{
			name: "VnetResourceGroup is Empty",
			testFunc: func(t *testing.T) {
				d := NewFakeDriver()
				d.cloud = &azure.Cloud{}
				d.cloud.SubscriptionID = "bar"
				d.cloud.NetworkResourceSubscriptionID = "bar"
				d.cloud.ResourceGroup = "fakeResourceGroup"
				d.cloud.VnetResourceGroup = ""
				actualOutput := d.populateSubnetPropertiesFromCloudConfig(SubnetProperties{
					VnetResourceGroup: "",
					VnetName:          "",
					SubnetName:        "",
				})
				expectedSubnetID := fmt.Sprintf(subnetTemplate, "bar", d.cloud.ResourceGroup, d.cloud.VnetName, d.cloud.SubnetName)
				expectedOutput := SubnetProperties{
					VnetResourceGroup: d.cloud.ResourceGroup,
					VnetName:          d.cloud.VnetName,
					SubnetName:        d.cloud.SubnetName,
					SubnetID:          expectedSubnetID,
				}
				assert.Equal(t, expectedOutput, actualOutput, "cloud.ResourceGroup should be used as the rg")
			},
		},
		{
			name: "VnetResourceGroup is not Empty",
			testFunc: func(t *testing.T) {
				d := NewFakeDriver()
				d.cloud = &azure.Cloud{}
				d.cloud.SubscriptionID = "bar"
				d.cloud.NetworkResourceSubscriptionID = "bar"
				d.cloud.ResourceGroup = "fakeResourceGroup"
				d.cloud.VnetResourceGroup = "fakeVnetResourceGroup"
				actualOutput := d.populateSubnetPropertiesFromCloudConfig(SubnetProperties{
					VnetResourceGroup: "",
					VnetName:          "",
					SubnetName:        "",
				})
				expectedSubnetID := fmt.Sprintf(subnetTemplate, "bar", d.cloud.VnetResourceGroup, d.cloud.VnetName, d.cloud.SubnetName)
				expectedOutput := SubnetProperties{
					VnetResourceGroup: d.cloud.VnetResourceGroup,
					VnetName:          d.cloud.VnetName,
					SubnetName:        d.cloud.SubnetName,
					SubnetID:          expectedSubnetID,
				}
				assert.Equal(t, expectedOutput, actualOutput, "cloud.VnetResourceGroup should be used as the rg")
			},
		},
		{
			name: "VnetResourceGroup, vnetName, subnetName is specified",
			testFunc: func(t *testing.T) {
				d := NewFakeDriver()
				d.cloud = &azure.Cloud{}
				d.cloud.SubscriptionID = "bar"
				d.cloud.NetworkResourceSubscriptionID = "bar"
				d.cloud.ResourceGroup = "fakeResourceGroup"
				d.cloud.VnetResourceGroup = "fakeVnetResourceGroup"
				actualOutput := d.populateSubnetPropertiesFromCloudConfig(SubnetProperties{
					VnetResourceGroup: "vnetrg",
					VnetName:          "vnetName",
					SubnetName:        "subnetName",
				})
				expectedSubnetID := fmt.Sprintf(subnetTemplate, "bar", "vnetrg", "vnetName", "subnetName")
				expectedOutput := SubnetProperties{
					VnetResourceGroup: "vnetrg",
					VnetName:          "vnetName",
					SubnetName:        "subnetName",
					SubnetID:          expectedSubnetID,
				}
				assert.Equal(t, expectedOutput, actualOutput, "VnetResourceGroup, vnetName, subnetName is specified")
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}
