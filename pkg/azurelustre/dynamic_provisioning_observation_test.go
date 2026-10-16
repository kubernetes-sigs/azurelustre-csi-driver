/*
Copyright 2026 The Kubernetes Authors.

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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/util"
)

func TestDynamicProvisioner_GetAmlFilesystem_ReusePreservesCustomerChanges(t *testing.T) {
	recorder := newMockAmlfsRecorder(nil)
	filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
	filesystem.Tags = map[string]*string{"customer": new("updated"), createdByTag: new("different-owner")}
	filesystem.Identity = &armstoragecache.AmlFilesystemIdentity{Type: new(armstoragecache.AmlFilesystemIdentityTypeNone)}
	filesystem.Properties.MaintenanceWindow = &armstoragecache.AmlFilesystemPropertiesMaintenanceWindow{
		DayOfWeek: new(armstoragecache.MaintenanceDayOfWeekTypeMonday), TimeOfDayUTC: new("06:00"),
	}
	filesystem.Properties.CurrentStorageCapacityTiB = new(float32(72))
	recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName] = filesystem
	before, err := json.Marshal(filesystem)
	require.NoError(t, err)
	properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
	properties.Tags = map[string]string{"customer": "stale", createdByTag: azureLustreDriverTag}
	properties.Identities = []string{"stale-identity"}
	properties.RequiredBytes = 50 * util.TiB
	properties.LimitBytes = 80 * util.TiB
	provisioner := newTestDynamicProvisioner(t, recorder)
	provisioner.mgmtClient, provisioner.vnetClient, provisioner.skusClient = nil, nil, nil

	for _, call := range []struct {
		name string
		run  func(context.Context, *AmlFilesystemProperties) (*AmlFilesystemResult, error)
	}{
		{name: "get", run: provisioner.GetAmlFilesystem},
		{name: "create", run: provisioner.CreateAmlFilesystem},
	} {
		t.Run(call.name, func(t *testing.T) {
			recorder.fakeCallCount = nil
			result, err := call.run(t.Context(), properties)
			require.NoError(t, err)
			assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: 72 * util.TiB}, result)
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)
			after, err := json.Marshal(recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName])
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "stale creation properties must not reconcile customer changes")
		})
	}
}

func TestDynamicProvisioner_ObserveAndCreateHandleProvisioningStates(t *testing.T) {
	expected := map[armstoragecache.AmlFilesystemProvisioningStateType]struct {
		lookupCode codes.Code
		createCode codes.Code
		deleted    bool
	}{
		armstoragecache.AmlFilesystemProvisioningStateTypeSucceeded: {lookupCode: codes.OK, createCode: codes.OK},
		armstoragecache.AmlFilesystemProvisioningStateTypeCreating:  {lookupCode: codes.Aborted, createCode: codes.Aborted},
		armstoragecache.AmlFilesystemProvisioningStateTypeUpdating:  {lookupCode: codes.Aborted, createCode: codes.Aborted},
		armstoragecache.AmlFilesystemProvisioningStateTypeDeleting:  {lookupCode: codes.Aborted, createCode: codes.Aborted},
		armstoragecache.AmlFilesystemProvisioningStateTypeFailed:    {lookupCode: codes.Unavailable, createCode: codes.Aborted, deleted: true},
		armstoragecache.AmlFilesystemProvisioningStateTypeCanceled:  {lookupCode: codes.Unavailable, createCode: codes.Unavailable},
	}
	for _, state := range armstoragecache.PossibleAmlFilesystemProvisioningStateTypeValues() {
		t.Run(string(state), func(t *testing.T) {
			outcome, covered := expected[state]
			require.True(t, covered, "every SDK provisioning state needs an explicit expectation")
			recorder := newMockAmlfsRecorder(nil)
			filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
			filesystem.Properties.ProvisioningState = new(state)
			filesystem.Tags = map[string]*string{createdByTag: new(azureLustreDriverTag), pvNameTag: new(expectedAmlFilesystemName)}
			recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName] = filesystem
			provisioner := newTestDynamicProvisioner(t, recorder)

			properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)

			observed, err := provisioner.GetAmlFilesystem(t.Context(), properties)

			assert.Equal(t, outcome.lookupCode, status.Code(err), "read-only lookup of %s must return %s; got %v", state, outcome.lookupCode, err)
			if outcome.lookupCode == codes.OK {
				assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: expectedClusterSize * util.TiB}, observed)
			} else {
				assert.Nil(t, observed)
				require.ErrorContains(t, err, *filesystem.ID)
			}
			if state == armstoragecache.AmlFilesystemProvisioningStateTypeFailed {
				require.ErrorIs(t, err, errAmlFilesystemFailed)
			}
			assert.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName, "lookup itself must never delete a failed backend")
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)

			result, err := provisioner.CreateAmlFilesystem(t.Context(), properties)

			assert.Equal(t, outcome.createCode, status.Code(err), "creation retry observing %s must return %s; got %v", state, outcome.createCode, err)
			if outcome.createCode == codes.OK {
				assert.Equal(t, observed, result, "a ready filesystem is reused")
			} else {
				assert.Nil(t, result)
				require.ErrorContains(t, err, *filesystem.ID)
			}
			calls := []string{"AmlFilesystemsServerTransport.Get", "AmlFilesystemsServerTransport.Get"}
			if outcome.deleted {
				calls = append(calls, "AmlFilesystemsServerTransport.BeginDelete")
				assert.NotContains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName)
			} else {
				assert.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName)
			}
			assert.Equal(t, calls, recorder.fakeCallCount)
		})
	}
}

func TestDynamicProvisioner_SucceededFilesystemRequiresAvailableHealth(t *testing.T) {
	states := armstoragecache.PossibleAmlFilesystemHealthStateTypeValues()
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
			filesystem.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateTypeSucceeded)
			filesystem.Properties.Health.State = new(state)
			recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName] = filesystem
			provisioner := newTestDynamicProvisioner(t, recorder)
			for _, call := range []struct {
				name string
				run  func(context.Context, *AmlFilesystemProperties) (*AmlFilesystemResult, error)
			}{
				{name: "lookup", run: provisioner.GetAmlFilesystem},
				{name: "create retry", run: provisioner.CreateAmlFilesystem},
			} {
				t.Run(call.name, func(t *testing.T) {
					result, err := call.run(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))
					if state == armstoragecache.AmlFilesystemHealthStateTypeAvailable {
						require.NoError(t, err)
						require.NotNil(t, result)
					} else {
						require.Equal(t, codes.Unavailable, status.Code(err), "Succeeded with health %s must remain retryable; got %v", state, err)
						assert.Nil(t, result)
						require.ErrorContains(t, err, string(state))
					}
				})
			}
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get", "AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)
		})
	}
	for _, testCase := range []struct {
		name    string
		health  *armstoragecache.AmlFilesystemHealth
		message string
	}{
		{name: "missing health object", health: nil, message: "missing health state"},
		{name: "missing health state", health: &armstoragecache.AmlFilesystemHealth{}, message: "missing health state"},
		{name: "empty health state", health: &armstoragecache.AmlFilesystemHealth{State: new(armstoragecache.AmlFilesystemHealthStateType(""))}, message: "missing health state"},
		{name: "unknown health state", health: &armstoragecache.AmlFilesystemHealth{State: new(armstoragecache.AmlFilesystemHealthStateType("FutureHealth"))}, message: "health is FutureHealth"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
			filesystem.Properties.Health = testCase.health
			recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName] = filesystem

			result, err := newTestDynamicProvisioner(t, recorder).CreateAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

			assert.Nil(t, result)
			require.Equal(t, codes.Unavailable, status.Code(err), "incomplete or unknown health must not be reported ready: %v", err)
			require.ErrorContains(t, err, testCase.message)
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)
		})
	}
}

func TestDynamicProvisioner_GetAmlFilesystem_IncompleteOrIncompatible(t *testing.T) {
	testCases := []struct {
		name    string
		mutate  func(*armstoragecache.AmlFilesystem, *AmlFilesystemProperties)
		code    codes.Code
		message string
	}{
		{name: "nil resource ID", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.ID = nil }, code: codes.Unavailable, message: "missing resource ID"},
		{name: "empty resource ID", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.ID = new("") }, code: codes.Unavailable, message: "missing resource ID"},
		{name: "different subscription", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.ID = new(amlFilesystemResourceID("another-subscription", expectedResourceGroupName, expectedAmlFilesystemName))
		}, code: codes.Unavailable, message: "unexpected resource ID"},
		{name: "different resource group", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.ID = new(amlFilesystemResourceID(expectedSubscriptionID, "another-group", expectedAmlFilesystemName))
		}, code: codes.Unavailable, message: "unexpected resource ID"},
		{name: "different resource name", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.ID = new(*f.ID + "-other")
		}, code: codes.Unavailable, message: "unexpected resource ID"},
		{name: "different resource provider", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.ID = new(strings.Replace(*f.ID, "Microsoft.StorageCache", "Microsoft.Other", 1))
		}, code: codes.Unavailable, message: "unexpected resource ID"},
		{name: "trailing resource segment", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.ID = new(*f.ID + "/child")
		}, code: codes.Unavailable, message: "unexpected resource ID"},
		{name: "nil properties", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.Properties = nil }, code: codes.Unavailable, message: "missing provisioning state"},
		{name: "nil state", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.ProvisioningState = nil
		}, code: codes.Unavailable, message: "missing provisioning state"},
		{name: "empty state", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateType(""))
		}, code: codes.Unavailable, message: "missing provisioning state"},
		{name: "unknown state", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.ProvisioningState = new(armstoragecache.AmlFilesystemProvisioningStateType("FutureState"))
		}, code: codes.Unavailable, message: "unknown provisioning state"},
		{name: "nil location", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.Location = nil }, code: codes.Unavailable, message: "missing location"},
		{name: "blank location", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.Location = new(" ") }, code: codes.Unavailable, message: "missing location"},
		{name: "nil SKU", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.SKU = nil }, code: codes.Unavailable, message: "missing SKU"},
		{name: "nil SKU name", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.SKU.Name = nil }, code: codes.Unavailable, message: "missing SKU"},
		{name: "empty SKU name", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.SKU.Name = new("") }, code: codes.Unavailable, message: "missing SKU"},
		{name: "nil subnet", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.FilesystemSubnet = nil
		}, code: codes.Unavailable, message: "missing filesystem subnet"},
		{name: "empty subnet", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.FilesystemSubnet = new("")
		}, code: codes.Unavailable, message: "missing filesystem subnet"},
		{name: "nil current capacity", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.CurrentStorageCapacityTiB = nil
		}, code: codes.Unavailable, message: "missing current storage capacity"},
		{name: "zero current capacity", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.CurrentStorageCapacityTiB = new(float32(0))
		}, code: codes.Unavailable, message: "invalid current AMLFS capacity"},
		{name: "negative current capacity", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.CurrentStorageCapacityTiB = new(float32(-1))
		}, code: codes.Unavailable, message: "invalid current AMLFS capacity"},
		{name: "overflowing byte capacity", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.CurrentStorageCapacityTiB = new(float32(1 << 23))
		}, code: codes.Unavailable, message: "invalid current AMLFS capacity"},
		{name: "half a byte is not a usable capacity", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.CurrentStorageCapacityTiB = new(float32(0.5) / float32(util.TiB))
		}, code: codes.Unavailable, message: "invalid current AMLFS capacity"},
		{name: "nil client info", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.Properties.ClientInfo = nil }, code: codes.Unavailable, message: "missing MGS address"},
		{name: "nil MGS", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.ClientInfo.MgsAddress = nil
		}, code: codes.Unavailable, message: "missing MGS address"},
		{name: "empty MGS", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.ClientInfo.MgsAddress = new("")
		}, code: codes.Unavailable, message: "missing MGS address"},
		{name: "malformed MGS", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.ClientInfo.MgsAddress = new("10.0.0.1#unexpected")
		}, code: codes.Unavailable, message: "invalid AMLFS MGS IPv4 address"},
		{name: "IPv6 MGS", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.ClientInfo.MgsAddress = new("::1")
		}, code: codes.Unavailable, message: "invalid AMLFS MGS IPv4 address"},
		{name: "IPv4 mapped IPv6 MGS", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.ClientInfo.MgsAddress = new("::ffff:10.0.0.1")
		}, code: codes.Unavailable, message: "invalid AMLFS MGS IPv4 address"},
		{name: "padded MGS", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.ClientInfo.MgsAddress = new(" 10.0.0.1 ")
		}, code: codes.Unavailable, message: "invalid AMLFS MGS IPv4 address"},
		{name: "missing requested zone", mutate: func(f *armstoragecache.AmlFilesystem, r *AmlFilesystemProperties) {
			f.Zones = nil
			r.Zone = "1"
		}, code: codes.Unavailable, message: "missing requested zone"},
		{name: "nil zone", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.Zones = []*string{nil} }, code: codes.Unavailable, message: "malformed AMLFS zone"},
		{name: "empty zone", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.Zones = []*string{new("")} }, code: codes.Unavailable, message: "malformed AMLFS zone"},
		{name: "multiple zones", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Zones = []*string{new("1"), new("2")}
		}, code: codes.Unavailable, message: "malformed AMLFS zone"},
		{name: "different location", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.Location = new("other-location") }, code: codes.AlreadyExists, message: "has location"},
		{name: "different SKU", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) { f.SKU.Name = new("other-sku") }, code: codes.AlreadyExists, message: "has SKU"},
		{name: "different network", mutate: func(f *armstoragecache.AmlFilesystem, _ *AmlFilesystemProperties) {
			f.Properties.FilesystemSubnet = new("other-subnet-id")
		}, code: codes.AlreadyExists, message: "has filesystem subnet"},
		{name: "different zone", mutate: func(f *armstoragecache.AmlFilesystem, r *AmlFilesystemProperties) {
			f.Zones, r.Zone = []*string{new("1")}, "2"
		}, code: codes.AlreadyExists, message: "has zone"},
	}
	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
			properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
			test.mutate(&filesystem, properties)
			recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName] = filesystem
			provisioner := newTestDynamicProvisioner(t, recorder)
			require.NotEqual(t, codes.OK, test.code, "each rejection case must state its expected status")
			require.NotEmpty(t, test.message, "each rejection case must identify the rejected field")
			for _, call := range []struct {
				name string
				run  func(context.Context, *AmlFilesystemProperties) (*AmlFilesystemResult, error)
			}{
				{name: "lookup", run: provisioner.GetAmlFilesystem},
				{name: "create retry", run: provisioner.CreateAmlFilesystem},
			} {
				t.Run(call.name, func(t *testing.T) {
					result, err := call.run(t.Context(), properties)
					assert.Nil(t, result)
					assert.Equal(t, test.code, status.Code(err), "rejected observation must return %s; got %v", test.code, err)
					require.ErrorContains(t, err, test.message)
					require.ErrorContains(t, err, amlFilesystemResourceID(expectedSubscriptionID, expectedResourceGroupName, expectedAmlFilesystemName))
				})
			}
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get", "AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)
			assert.Contains(t, recorder.recordedAmlfsConfigurations, expectedAmlFilesystemName)
		})
	}
}

func TestDynamicProvisioner_GetAmlFilesystem_CurrentCapacityRange(t *testing.T) {
	for _, test := range []struct {
		name            string
		originalTiB     *float32
		currentTiB      float32
		required, limit int64
		code            codes.Code
		expectedBytes   int64
	}{
		{name: "exact requested bounds", originalTiB: new(float32(48)), currentTiB: 48, required: 48 * util.TiB, limit: 48 * util.TiB, code: codes.OK, expectedBytes: 48 * util.TiB},
		{name: "observed capacity is within requested bounds", originalTiB: new(float32(48)), currentTiB: 48, required: 45 * util.TiB, limit: 50 * util.TiB, code: codes.OK, expectedBytes: 48 * util.TiB},
		{name: "return expanded capacity rather than original size", originalTiB: new(float32(48)), currentTiB: 72, required: 70 * util.TiB, limit: 80 * util.TiB, code: codes.OK, expectedBytes: 72 * util.TiB},
		{name: "zero bounds leave capacity unconstrained", originalTiB: new(float32(48)), currentTiB: 72, required: 0, limit: 0, code: codes.OK, expectedBytes: 72 * util.TiB},
		{name: "zero limit leaves the upper bound unconstrained", originalTiB: new(float32(48)), currentTiB: 72, required: 64 * util.TiB, limit: 0, code: codes.OK, expectedBytes: 72 * util.TiB},
		{name: "capacity below the requested minimum is incompatible", originalTiB: new(float32(48)), currentTiB: 48, required: 50 * util.TiB, code: codes.AlreadyExists},
		{name: "expansion beyond the requested limit is incompatible", originalTiB: new(float32(48)), currentTiB: 72, limit: 64 * util.TiB, code: codes.AlreadyExists},
		{name: "missing original size does not hide current capacity", originalTiB: nil, currentTiB: 72, required: 64 * util.TiB, code: codes.OK, expectedBytes: 72 * util.TiB},
		{name: "synthetic fractional TiB converts to whole bytes", originalTiB: new(float32(4.5)), currentTiB: 4.5, required: 4 * util.TiB, limit: 5 * util.TiB, code: codes.OK, expectedBytes: 9 * util.TiB / 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
			filesystem.Properties.CurrentStorageCapacityTiB = new(test.currentTiB)
			filesystem.Properties.StorageCapacityTiB = test.originalTiB
			recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName] = filesystem
			properties := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
			properties.RequiredBytes, properties.LimitBytes = test.required, test.limit
			provisioner := newTestDynamicProvisioner(t, recorder)
			result, err := provisioner.GetAmlFilesystem(t.Context(), properties)
			assert.Equal(t, test.code, status.Code(err), "capacity compatibility must return %s; got %v", test.code, err)
			if test.code == codes.OK {
				assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: test.expectedBytes}, result)
			} else {
				assert.Nil(t, result)
			}
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)
		})
	}
}

func TestDynamicProvisioner_GetAmlFilesystem_AllowsARMResponseCasingDifferences(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*armstoragecache.AmlFilesystem)
	}{
		{name: "resource ID", mutate: func(f *armstoragecache.AmlFilesystem) { f.ID = new(strings.ToUpper(*f.ID)) }},
		{name: "location", mutate: func(f *armstoragecache.AmlFilesystem) { f.Location = new(strings.ToUpper(*f.Location)) }},
		{name: "SKU", mutate: func(f *armstoragecache.AmlFilesystem) { f.SKU.Name = new(strings.ToUpper(*f.SKU.Name)) }},
		{name: "subnet ID", mutate: func(f *armstoragecache.AmlFilesystem) {
			f.Properties.FilesystemSubnet = new(strings.ToUpper(*f.Properties.FilesystemSubnet))
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
			testCase.mutate(&filesystem)
			recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName] = filesystem

			result, err := newTestDynamicProvisioner(t, recorder).GetAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

			require.NoError(t, err, "ARM response casing must not make the same backend incompatible")
			assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: expectedClusterSize * util.TiB}, result)
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)
		})
	}
}

func TestDynamicProvisioner_GetAmlFilesystem_UnspecifiedZoneAcceptsZonalResource(t *testing.T) {
	recorder := newMockAmlfsRecorder(nil)
	filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
	filesystem.Zones = []*string{new("1")}
	recorder.recordedAmlfsConfigurations[expectedAmlFilesystemName] = filesystem
	provisioner := newTestDynamicProvisioner(t, recorder)
	requested := buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName)
	requested.Zone = ""

	result, err := provisioner.GetAmlFilesystem(t.Context(), requested)

	require.NoError(t, err, "an unspecified zone does not constrain an otherwise compatible resource")
	assert.Equal(t, &AmlFilesystemResult{MGSAddress: expectedMgsAddress, CapacityBytes: expectedClusterSize * util.TiB}, result)
	assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)
}

func TestDynamicProvisioner_GetAmlFilesystem_LookupErrorsAreNotAbsence(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code codes.Code
	}{
		{name: "untyped", err: errors.New("404 ResourceNotFound"), code: codes.Unknown},
		{name: "untyped status", err: status.Error(codes.NotFound, "ResourceNotFound"), code: codes.NotFound},
		{name: "wrong HTTP status", err: &azcore.ResponseError{StatusCode: http.StatusServiceUnavailable, ErrorCode: "ResourceNotFound"}, code: codes.Unavailable},
		{name: "different 404", err: &azcore.ResponseError{StatusCode: http.StatusNotFound, ErrorCode: "ResourceGroupNotFound"}, code: codes.NotFound},
		{name: "empty code", err: &azcore.ResponseError{StatusCode: http.StatusNotFound}, code: codes.NotFound},
		{name: "wrong case", err: &azcore.ResponseError{StatusCode: http.StatusNotFound, ErrorCode: "resourceNotFound"}, code: codes.NotFound},
		{name: "canceled", err: fmt.Errorf("wrapped: %w", context.Canceled), code: codes.Canceled},
		{name: "deadline", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), code: codes.DeadlineExceeded},
		{name: "transport", err: errors.New("connection reset"), code: codes.Unknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			recorder.getError = test.err
			provisioner := newTestDynamicProvisioner(t, recorder)
			result, err := provisioner.GetAmlFilesystem(t.Context(), buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))
			assert.Nil(t, result)
			require.Error(t, err, "this error must not become the nil,nil absence result")
			assert.Equal(t, test.code, status.Code(err), "lookup must retain %s; got %v", test.code, err)
			require.ErrorContains(t, err, amlfsTestResourceID())
			assert.Equal(t, []string{"AmlFilesystemsServerTransport.Get"}, recorder.fakeCallCount)
		})
	}
}

func TestDynamicProvisioner_GetAmlFilesystem_RejectsInvalidRequestOrConfigurationBeforeARM(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*DynamicProvisioner, *AmlFilesystemProperties) *AmlFilesystemProperties
		code    codes.Code
		message string
	}{
		{name: "nil properties", mutate: func(_ *DynamicProvisioner, _ *AmlFilesystemProperties) *AmlFilesystemProperties { return nil }, code: codes.InvalidArgument, message: "properties must be provided"},
		{name: "nil client", mutate: func(d *DynamicProvisioner, r *AmlFilesystemProperties) *AmlFilesystemProperties {
			d.amlFilesystemsClient = nil
			return r
		}, code: codes.Internal, message: "client is nil"},
		{name: "unconfigured subscription", mutate: func(d *DynamicProvisioner, r *AmlFilesystemProperties) *AmlFilesystemProperties {
			d.subscriptionID = ""
			return r
		}, code: codes.Internal, message: "subscription ID is not configured"},
		{name: "missing group", mutate: func(_ *DynamicProvisioner, r *AmlFilesystemProperties) *AmlFilesystemProperties {
			r.ResourceGroupName = ""
			return r
		}, code: codes.InvalidArgument, message: "resource group and filesystem name must be provided"},
		{name: "missing name", mutate: func(_ *DynamicProvisioner, r *AmlFilesystemProperties) *AmlFilesystemProperties {
			r.AmlFilesystemName = ""
			return r
		}, code: codes.InvalidArgument, message: "resource group and filesystem name must be provided"},
		{name: "negative required", mutate: func(_ *DynamicProvisioner, r *AmlFilesystemProperties) *AmlFilesystemProperties {
			r.RequiredBytes = -1
			return r
		}, code: codes.InvalidArgument, message: "invalid AMLFS capacity range"},
		{name: "negative limit", mutate: func(_ *DynamicProvisioner, r *AmlFilesystemProperties) *AmlFilesystemProperties {
			r.LimitBytes = -1
			return r
		}, code: codes.InvalidArgument, message: "invalid AMLFS capacity range"},
		{name: "inverted range", mutate: func(_ *DynamicProvisioner, r *AmlFilesystemProperties) *AmlFilesystemProperties {
			r.RequiredBytes, r.LimitBytes = 2, 1
			return r
		}, code: codes.InvalidArgument, message: "invalid AMLFS capacity range"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMockAmlfsRecorder(nil)
			provisioner := newTestDynamicProvisioner(t, recorder)
			properties := test.mutate(provisioner, buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))
			result, err := provisioner.GetAmlFilesystem(t.Context(), properties)
			assert.Nil(t, result)
			assert.Equal(t, test.code, status.Code(err), "invalid request/configuration must return %s; got %v", test.code, err)
			require.Error(t, err)
			require.ErrorContains(t, err, test.message)
			assert.Empty(t, recorder.fakeCallCount)
		})
	}
}

func TestAmlFilesystemResult_RejectsSyntheticNonFiniteCapacity(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value float32
	}{
		{name: "NaN", value: float32(math.NaN())},
		{name: "positive infinity", value: float32(math.Inf(1))},
		{name: "negative infinity", value: float32(math.Inf(-1))},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// JSON cannot encode these values; this exercises the in-memory response validator.
			filesystem := readyTestAmlFilesystem(expectedAmlFilesystemName)
			filesystem.Properties.CurrentStorageCapacityTiB = new(testCase.value)
			provisioner := &DynamicProvisioner{subscriptionID: expectedSubscriptionID}

			result, err := provisioner.amlFilesystemResult(&filesystem, buildExpectedAmlFilesystemProperties(expectedAmlFilesystemName))

			assert.Nil(t, result)
			assert.Equal(t, codes.Unavailable, status.Code(err), "non-finite capacity must not become a usable CSI capacity: %v", err)
		})
	}
}
