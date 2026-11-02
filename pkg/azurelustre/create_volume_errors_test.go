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
	"errors"
	"maps"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/util"
)

type creationFailureProvisioner struct {
	FakeDynamicProvisioner
	lookupError error
	createError error
	calls       []string
}

func (p *creationFailureProvisioner) GetAmlFilesystem(context.Context, *AmlFilesystemProperties) (*AmlFilesystemResult, error) {
	p.calls = append(p.calls, "lookup")
	return nil, p.lookupError
}

func (p *creationFailureProvisioner) CreateAmlFilesystem(ctx context.Context, properties *AmlFilesystemProperties) (*AmlFilesystemResult, error) {
	p.calls = append(p.calls, "create")
	if p.createError != nil {
		return nil, p.createError
	}
	return p.FakeDynamicProvisioner.CreateAmlFilesystem(ctx, properties)
}

func TestCreateVolumeKeepsUncertainBackendFailuresRetryable(t *testing.T) {
	tests := []struct {
		name            string
		lookupError     error
		createError     error
		expectedMessage string
		expectedCalls   []string
	}{
		{
			name:            "transport fails while observing a possibly existing filesystem",
			lookupError:     errors.New("connection reset while observing AMLFS"),
			expectedMessage: "connection reset while observing AMLFS",
			expectedCalls:   []string{"lookup"},
		},
		{
			name:            "ARM observation reports an internal service error",
			lookupError:     status.Error(codes.Internal, "ARM GET returned HTTP 500"),
			expectedMessage: "ARM GET returned HTTP 500",
			expectedCalls:   []string{"lookup"},
		},
		{
			name:            "creation loses its response after the filesystem was initially absent",
			createError:     errors.New("connection reset while receiving the create result"),
			expectedMessage: "connection reset while receiving the create result",
			expectedCalls:   []string{"lookup", "create"},
		},
		{
			name:            "creation reports an internal service error",
			createError:     status.Error(codes.Internal, "ARM creation polling returned HTTP 500"),
			expectedMessage: "ARM creation polling returned HTTP 500",
			expectedCalls:   []string{"lookup", "create"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			driver := NewFakeDriver(t)
			provisioner := &creationFailureProvisioner{
				lookupError: tc.lookupError,
				createError: tc.createError,
			}
			driver.dynamicProvisioner = provisioner
			request := buildDynamicProvCreateVolumeRequest()

			response, err := driver.CreateVolume(t.Context(), request)

			require.ErrorContains(t, err, tc.expectedMessage)
			assert.Equal(t, codes.Unavailable, status.Code(err), "uncertain Azure work must not be reported as finished")
			assert.Nil(t, response)
			assert.Equal(t, tc.expectedCalls, provisioner.calls)
			assert.NotContains(t, provisioner.fakeCallCount, "DeleteAmlFilesystem")
		})
	}
}

func TestRetryableCreateErrorPreservesOtherClassifications(t *testing.T) {
	for _, code := range []codes.Code{
		codes.InvalidArgument,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.NotFound,
		codes.ResourceExhausted,
		codes.AlreadyExists,
		codes.Aborted,
		codes.Canceled,
		codes.DeadlineExceeded,
		codes.Unavailable,
	} {
		t.Run(code.String(), func(t *testing.T) {
			original := status.Error(code, "original backend diagnostic")

			err := retryableCreateError(original)

			require.EqualError(t, err, original.Error())
			assert.Equal(t, code, status.Code(err))
		})
	}
}

func TestCreateVolumeAdmissionRetainsObservedFailedBackend(t *testing.T) {
	tests := []struct {
		name            string
		parameters      map[string]string
		capacity        *csi.CapacityRange
		expectedMessage string
	}{
		{
			name:            "SKU no longer advertised",
			parameters:      map[string]string{VolumeContextSkuName: "retired-sku"},
			expectedMessage: "sku-name must be one of",
		},
		{
			name:            "requested zone no longer advertised",
			parameters:      map[string]string{VolumeContextZone: "retired-zone"},
			expectedMessage: "zone retired-zone must be one of",
		},
		{
			name:            "requested zone cannot be verified from an empty zone list",
			parameters:      map[string]string{VolumeContextLocation: emptyZonesLocation},
			expectedMessage: "no zones available",
		},
		{
			name:            "current SKU maximum is below the requested capacity",
			capacity:        &csi.CapacityRange{RequiredBytes: 9000 * util.TiB},
			expectedMessage: "exceeds maximum capacity",
		},
		{
			name:            "current capacity increment exceeds the request limit",
			capacity:        &csi.CapacityRange{RequiredBytes: 1 * util.TiB, LimitBytes: 2 * util.TiB},
			expectedMessage: "greater than capacity limit",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, observation := range []struct {
				name         string
				lookupError  error
				expectedCode codes.Code
			}{
				{name: "verified absent", expectedCode: codes.InvalidArgument},
				{name: "matching Failed backend", lookupError: errAmlFilesystemFailed, expectedCode: codes.Unavailable},
			} {
				t.Run(observation.name, func(t *testing.T) {
					driver := NewFakeDriver(t)
					provisioner := &creationFailureProvisioner{lookupError: observation.lookupError}
					driver.dynamicProvisioner = provisioner
					request := buildDynamicProvCreateVolumeRequest()
					maps.Copy(request.GetParameters(), tc.parameters)
					request.CapacityRange = tc.capacity

					response, err := driver.CreateVolume(t.Context(), request)

					require.ErrorContains(t, err, tc.expectedMessage)
					assert.Equal(t, observation.expectedCode, status.Code(err),
						"fresh admission rejects the request, but a Failed backend still needs recovery")
					assert.Nil(t, response)
					assert.Equal(t, []string{"lookup"}, provisioner.calls, "failed admission must not bypass validation or mutate storage")
					if observation.lookupError != nil {
						require.ErrorContains(t, err, "Failed recovery is blocked by admission")
						require.ErrorContains(t, err, request.GetName())
					}
				})
			}
		})
	}
}

func TestCreateVolumePreservesDefiniteBackendErrors(t *testing.T) {
	for _, tc := range []struct {
		name         string
		lookupError  error
		createError  error
		expectedCode codes.Code
	}{
		{
			name:         "observed incompatible existing filesystem",
			lookupError:  status.Error(codes.AlreadyExists, "observed filesystem SKU differs from the request"),
			expectedCode: codes.AlreadyExists,
		},
		{
			name:         "fresh create rejected for invalid parameters",
			createError:  status.Error(codes.InvalidArgument, "ARM rejected the requested SKU"),
			expectedCode: codes.InvalidArgument,
		},
		{
			name:         "fresh create rejected for missing permission",
			createError:  status.Error(codes.PermissionDenied, "ARM denied the create request"),
			expectedCode: codes.PermissionDenied,
		},
		{
			name:         "fresh create rejected for invalid authentication",
			createError:  status.Error(codes.Unauthenticated, "ARM rejected the credentials"),
			expectedCode: codes.Unauthenticated,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			driver := NewFakeDriver(t)
			provisioner := &creationFailureProvisioner{lookupError: tc.lookupError, createError: tc.createError}
			driver.dynamicProvisioner = provisioner

			response, err := driver.CreateVolume(t.Context(), buildDynamicProvCreateVolumeRequest())

			require.Error(t, err)
			assert.Nil(t, response)
			assert.Equal(t, tc.expectedCode, status.Code(err), "unresolved-work handling must not change definite CSI errors")
			if tc.lookupError != nil {
				assert.Equal(t, []string{"lookup"}, provisioner.calls, "an incompatible filesystem must not be overwritten")
			} else {
				assert.Equal(t, []string{"lookup", "create"}, provisioner.calls)
			}
		})
	}
}
