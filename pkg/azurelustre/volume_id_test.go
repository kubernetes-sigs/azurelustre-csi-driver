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
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type volumeIDProvisioner struct {
	FakeDynamicProvisioner
	deletedGroup string
	deletedName  string
}

func (p *volumeIDProvisioner) DeleteAmlFilesystem(ctx context.Context, group, name string) error {
	p.deletedGroup, p.deletedName = group, name
	return p.FakeDynamicProvisioner.DeleteAmlFilesystem(ctx, group, name)
}

func TestVolumeIDMountPathPreservesDeletionIdentity(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		mode := "existing filesystem"
		if dynamic {
			mode = "dynamically created filesystem"
		}
		for _, subdirectory := range []string{"ordinary/path", "team%23literal"} {
			t.Run(fmt.Sprintf("%s/subdirectory=%s", mode, subdirectory), func(t *testing.T) {
				d := NewFakeDriver(t)
				provisioner := &volumeIDProvisioner{}
				d.dynamicProvisioner = provisioner
				request := buildCreateVolumeRequest()
				if dynamic {
					request = buildDynamicProvCreateVolumeRequest()
					delete(request.GetParameters(), VolumeContextMGSIPAddress)
				} else {
					request.Parameters[VolumeContextMGSIPAddress] = "10.0.0.4"
				}
				request.Parameters[VolumeContextSubDir] = subdirectory
				originalParameters := maps.Clone(request.GetParameters())

				response, err := d.CreateVolume(t.Context(), request)

				require.NoError(t, err)
				assert.Equal(t, originalParameters, request.GetParameters(), "creation must not rewrite the caller's parameters")
				id := response.GetVolume().GetVolumeId()
				assert.Equal(t, 5, strings.Count(id, separator), "new writers retain six positional fields")
				assert.Equal(t, subdirectory, response.GetVolume().GetVolumeContext()[VolumeContextSubDir])
				parsed, err := getLustreVolFromID(id)
				require.NoError(t, err)
				expectedAddress := "10.0.0.4"
				expectedDynamicFlag := "f"
				if dynamic {
					expectedAddress = "127.0.0.2"
					expectedDynamicFlag = "t"
					require.Len(t, provisioner.Filesystems, 1)
					assert.Equal(t, request.GetName(), provisioner.Filesystems[0].AmlFilesystemName)
					assert.Equal(t, 1, provisioner.fakeCallCount["CreateAmlFilesystem"])
				} else {
					assert.Empty(t, provisioner.fakeCallCount)
				}
				assert.Equal(t, expectedAddress, parsed.mgsIPAddress, "the final ID must use the connection details returned by creation")
				assert.Equal(t, expectedAddress, response.GetVolume().GetVolumeContext()[VolumeContextMGSIPAddress])
				assert.Equal(t, expectedDynamicFlag, response.GetVolume().GetVolumeContext()[VolumeContextInternalDynamicallyCreated])
				assert.Equal(t, subdirectory, parsed.subDir, "the ID must retain the literal directory")
				assert.Equal(t, dynamic, parsed.createdByDynamicProvisioning)
				assert.Equal(t, request.GetParameters()[VolumeContextResourceGroupName], parsed.resourceGroupName)
				mountParameters, err := getVolume(id, response.GetVolume().GetVolumeContext())
				require.NoError(t, err)
				assert.Equal(t, subdirectory, mountParameters.subDir, "node-side parameter parsing must retain the literal path; this test does not mount")

				provisioner.fakeCallCount = make(map[string]int)
				_, err = d.DeleteVolume(t.Context(), &csi.DeleteVolumeRequest{VolumeId: id})
				require.NoError(t, err)
				if dynamic {
					assert.Equal(t, 1, provisioner.fakeCallCount["DeleteAmlFilesystem"])
					assert.Equal(t, request.GetParameters()[VolumeContextResourceGroupName], provisioner.deletedGroup)
					assert.Equal(t, request.GetName(), provisioner.deletedName)
					assert.Empty(t, provisioner.Filesystems, "delete must target the filesystem that create actually made")
				} else {
					assert.Empty(t, provisioner.fakeCallCount, "static paths must never become dynamic deletion authority")
				}
			})
		}
	}
}

func TestVolumeIDSeparatorValidationPrecedesAzure(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		request func(*Driver) *csi.CreateVolumeRequest
	}{
		{name: "static volume name contains a delimiter", request: func(_ *Driver) *csi.CreateVolumeRequest {
			r := buildCreateVolumeRequest()
			r.Name = "invalid#name"
			return r
		}},
		{name: "dynamic volume name contains a delimiter", request: func(_ *Driver) *csi.CreateVolumeRequest {
			r := buildDynamicProvCreateVolumeRequest()
			r.Name = "invalid#name"
			return r
		}},
		{name: "static MGS address contains deletion-field text", request: func(_ *Driver) *csi.CreateVolumeRequest {
			r := buildCreateVolumeRequest()
			r.Parameters[VolumeContextMGSIPAddress] = "10.0.0.4#t#group"
			return r
		}},
		{name: "static subdirectory contains deletion-field text", request: func(_ *Driver) *csi.CreateVolumeRequest {
			r := buildCreateVolumeRequest()
			r.Parameters[VolumeContextSubDir] = "team#t#wrong-group"
			return r
		}},
		{name: "dynamic subdirectory contains deletion-field text", request: func(_ *Driver) *csi.CreateVolumeRequest {
			r := buildDynamicProvCreateVolumeRequest()
			r.Parameters[VolumeContextSubDir] = "team#f#wrong-group"
			return r
		}},
		{name: "empty subdirectory is rejected before dynamic creation", request: func(_ *Driver) *csi.CreateVolumeRequest {
			r := buildDynamicProvCreateVolumeRequest()
			r.Parameters[VolumeContextSubDir] = ""
			return r
		}},
		{name: "root subdirectory is rejected before dynamic creation", request: func(_ *Driver) *csi.CreateVolumeRequest {
			r := buildDynamicProvCreateVolumeRequest()
			r.Parameters[VolumeContextSubDir] = "/"
			return r
		}},
		{name: "requested resource group contains a delimiter", request: func(_ *Driver) *csi.CreateVolumeRequest {
			r := buildDynamicProvCreateVolumeRequest()
			r.Parameters[VolumeContextResourceGroupName] = "invalid#group"
			return r
		}},
		{name: "configured default resource group contains a delimiter", request: func(d *Driver) *csi.CreateVolumeRequest {
			r := buildDynamicProvCreateVolumeRequest()
			delete(r.GetParameters(), VolumeContextResourceGroupName)
			d.resourceGroup = "invalid#group"
			return r
		}},
		{name: "volume name contains NUL", request: func(_ *Driver) *csi.CreateVolumeRequest {
			r := buildCreateVolumeRequest()
			r.Name = "invalid\x00name"
			return r
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			d := NewFakeDriver(t)
			provisioner := &FakeDynamicProvisioner{}
			d.dynamicProvisioner = provisioner
			request := testCase.request(d)

			response, err := d.CreateVolume(t.Context(), request)

			assert.Nil(t, response)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "invalid identity must be rejected before Azure access: %v", err)
			assert.Empty(t, provisioner.fakeCallCount, "identity validation must happen before even SKU lookup")
		})
	}
}

func TestVolumeIDValidationChecksEveryCaseVariant(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		key          string
		validValue   string
		invalidValue string
	}{
		{name: "subdirectory delimiter", key: VolumeContextSubDir, validValue: "ordinary/path", invalidValue: "team#f#wrong-group"},
		{name: "MGS delimiter", key: VolumeContextMGSIPAddress, validValue: "10.0.0.4", invalidValue: "10.0.0.4#t#group"},
		{name: "MGS NUL", key: VolumeContextMGSIPAddress, validValue: "10.0.0.4", invalidValue: "10.0.0.4\x00"},
		{name: "resource group delimiter", key: VolumeContextResourceGroupName, validValue: "test-resource-group", invalidValue: "wrong#group"},
		{name: "resource group NUL", key: VolumeContextResourceGroupName, validValue: "test-resource-group", invalidValue: "wrong\x00group"},
	} {
		for _, invalidKey := range []string{testCase.key, strings.ToUpper(testCase.key)} {
			t.Run(testCase.name+"/"+invalidKey, func(t *testing.T) {
				driver := NewFakeDriver(t)
				provisioner := &FakeDynamicProvisioner{}
				driver.dynamicProvisioner = provisioner
				request := buildDynamicProvCreateVolumeRequest()
				request.Parameters[testCase.key] = testCase.validValue
				request.Parameters[strings.ToUpper(testCase.key)] = testCase.validValue
				request.Parameters[invalidKey] = testCase.invalidValue
				originalParameters := maps.Clone(request.GetParameters())

				id, err := createVolumeIDFromParams(request.GetName(), request.GetParameters())

				assert.Empty(t, id)
				assert.Equal(t, codes.InvalidArgument, status.Code(err), "every supplied value must be checked, regardless of map iteration order")

				response, err := driver.CreateVolume(t.Context(), request)

				assert.Nil(t, response)
				assert.Equal(t, codes.InvalidArgument, status.Code(err), "invalid aliases must be rejected before Azure work")
				assert.Empty(t, provisioner.fakeCallCount, "defaulting must not hide an invalid alias before validation")
				assert.Empty(t, provisioner.Filesystems)
				assert.Equal(t, originalParameters, request.GetParameters())
			})
		}
	}
}

func TestVolumeIDValidationAcceptsMatchingCaseVariants(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		t.Run(fmt.Sprintf("dynamic=%t", dynamic), func(t *testing.T) {
			driver := NewFakeDriver(t)
			request := buildCreateVolumeRequest()
			if dynamic {
				request = buildDynamicProvCreateVolumeRequest()
			}
			for _, key := range []string{VolumeContextSubDir, VolumeContextMGSIPAddress, VolumeContextResourceGroupName} {
				if value, exists := request.GetParameters()[key]; exists {
					request.Parameters[strings.ToUpper(key)] = value
				}
			}
			originalParameters := maps.Clone(request.GetParameters())

			response, err := driver.CreateVolume(t.Context(), request)

			require.NoError(t, err)
			volume, err := getLustreVolFromID(response.GetVolume().GetVolumeId())
			require.NoError(t, err)
			assert.Equal(t, request.GetParameters()[VolumeContextSubDir], volume.subDir)
			assert.Equal(t, request.GetParameters()[VolumeContextResourceGroupName], volume.resourceGroupName)
			assert.Equal(t, dynamic, volume.createdByDynamicProvisioning)
			assert.Equal(t, originalParameters, request.GetParameters())
		})
	}
}

func TestVolumeIDWriterKeepsTheEstablishedSixFieldLayout(t *testing.T) {
	for _, testCase := range []struct{ name, subDir, expectedID string }{
		{name: "ordinary subdirectory retains its slot", subDir: "/ordinary/path/", expectedID: "volume#lustrefs#10.0.0.4#ordinary/path#t#group"},
		{name: "percent characters remain literal", subDir: "directory%23literal", expectedID: "volume#lustrefs#10.0.0.4#directory%23literal#t#group"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			parameters := map[string]string{
				VolumeContextMGSIPAddress: "10.0.0.4", VolumeContextSubDir: testCase.subDir,
				VolumeContextInternalDynamicallyCreated: "t", VolumeContextResourceGroupName: "group",
			}

			id, err := createVolumeIDFromParams("volume", parameters)

			require.NoError(t, err)
			assert.Equal(t, testCase.expectedID, id, "pin the serialized layout consumed by existing readers")
		})
	}
}

func TestVolumeIDTrailingFieldsPreserveParsingAndDeletion(t *testing.T) {
	for _, id := range []string{
		"volume#lustrefs#10.0.0.4#path#t#group",
		"volume#lustrefs#10.0.0.4#path#f#",
	} {
		t.Run(id, func(t *testing.T) {
			original, err := getLustreVolFromID(id)
			require.NoError(t, err)

			for _, suffix := range []string{"#future-field", "#future-field#future-value", "#t#other-group"} {
				t.Run(suffix, func(t *testing.T) {
					extendedID := id + suffix
					withFutureFields, err := getLustreVolFromID(extendedID)

					require.NoError(t, err, "additional trailing fields must not invalidate the known identity")
					expected := *original
					expected.id = extendedID
					assert.Equal(t, &expected, withFutureFields, "retain the full opaque ID while preserving every parsed backend field")

					driver := NewFakeDriver(t)
					provisioner := &volumeIDProvisioner{}
					driver.dynamicProvisioner = provisioner

					_, err = driver.DeleteVolume(t.Context(), &csi.DeleteVolumeRequest{VolumeId: extendedID})

					require.NoError(t, err)
					if original.createdByDynamicProvisioning {
						assert.Equal(t, map[string]int{"DeleteAmlFilesystem": 1}, provisioner.fakeCallCount)
						assert.Equal(t, "group", provisioner.deletedGroup)
						assert.Equal(t, "volume", provisioner.deletedName)
					} else {
						assert.Empty(t, provisioner.fakeCallCount, "trailing fields must not enable deletion for a static volume")
					}
				})
			}
		})
	}
}
