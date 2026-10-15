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

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetClient(t *testing.T) {
	t.Setenv("AZURE_AUTHORITY_HOST", "https://login.microsoftonline.com/")
	client, err := GetClient("AzurePublicCloud", "test-subscription", "test-client", "test-tenant", "test-secret")
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.NotNil(t, client.groupsClient)
	assert.Equal(t, "test-subscription", client.subscriptionID)
}

func TestGetClientErrors(t *testing.T) {
	t.Setenv("AZURE_AUTHORITY_HOST", "https://login.microsoftonline.com/")
	tests := []struct {
		name    string
		cloud   string
		tenant  string
		message string
	}{
		{name: "unknown cloud", cloud: "invalid-cloud", tenant: "test-tenant", message: "INVALID-CLOUD"},
		{name: "invalid tenant", cloud: "AzurePublicCloud", tenant: "invalid/tenant", message: "failed to create Azure credential"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := GetClient(test.cloud, "test-subscription", "test-client", test.tenant, "test-secret")
			require.ErrorContains(t, err, test.message)
			assert.Nil(t, client, "construction failure must not return a client")
		})
	}
}

func TestGetClientFactoryError(t *testing.T) {
	options := &arm.ClientOptions{}
	options.Cloud = cloud.Configuration{ActiveDirectoryAuthorityHost: "https://example.invalid"}
	client, err := getClient(azure.PublicCloud, "test-subscription", nil, options)
	require.ErrorContains(t, err, "failed to create resource groups client")
	require.ErrorContains(t, err, "missing Azure Resource Manager configuration")
	assert.Nil(t, client, "factory failure must not return a client")
}
