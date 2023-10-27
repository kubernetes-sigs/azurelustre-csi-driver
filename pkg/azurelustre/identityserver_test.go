/*
Copyright 2020 The Kubernetes Authors.

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
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetPluginInfo(t *testing.T) {
	// Check with correct arguments
	d := NewFakeDriver()
	req := csi.GetPluginInfoRequest{}
	resp, err := d.GetPluginInfo(context.Background(), &req)
	assert.NoError(t, err)
	assert.Equal(t, fakeDriverName, resp.Name)
	assert.Equal(t, vendorVersion, resp.GetVendorVersion())
}

func TestGetPluginInfo_Err_NoDriverName(t *testing.T) {
	// Check error when driver name is empty
	d := NewFakeDriver()
	d.Name = ""
	req := csi.GetPluginInfoRequest{}
	resp, err := d.GetPluginInfo(context.Background(), &req)
	assert.Error(t, err)
	assert.Nil(t, resp)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Unavailable, grpcStatus.Code())
	assert.ErrorContains(t, err, "Driver name")
}

func TestGetPluginInfo_Err_NoVersion(t *testing.T) {
	// Check error when version is empty
	d := NewFakeDriver()
	d.Version = ""
	req := csi.GetPluginInfoRequest{}
	resp, err := d.GetPluginInfo(context.Background(), &req)
	assert.Error(t, err)
	assert.Nil(t, resp)
	grpcStatus, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Unavailable, grpcStatus.Code())
	assert.ErrorContains(t, err, "version")
}

func TestProbe(t *testing.T) {
	d := NewFakeDriver()
	req := csi.ProbeRequest{}
	resp, err := d.Probe(context.Background(), &req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, resp.XXX_sizecache, int32(0))
	assert.Equal(t, true, resp.Ready.Value)
}

func TestGetPluginCapabilities(t *testing.T) {
	d := NewFakeDriver()
	req := csi.GetPluginCapabilitiesRequest{}
	resp, err := d.GetPluginCapabilities(context.Background(), &req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, int32(0), resp.XXX_sizecache)
}
