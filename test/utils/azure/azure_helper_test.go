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
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetClient(t *testing.T) {
	t.Setenv("AZURE_AUTHORITY_HOST", "http://wrong-authority.invalid/")
	for _, name := range []string{"AzurePublicCloud", "azurecloud", "AzureUSGovernment", "AzureUSGovernmentCloud", "AzureChinaCloud"} {
		t.Run(name, func(t *testing.T) {
			client, err := GetClient(name, "test-subscription", "test-client", "test-tenant", "test-secret")
			require.NoError(t, err, "the explicit cloud must supply its own credential authority")
			require.NotNil(t, client)
			assert.NotNil(t, client.groupsClient)
			assert.Equal(t, "test-subscription", client.subscriptionID)
		})
	}
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
		{name: "retired German cloud", cloud: "azureGermanCloud", tenant: "test-tenant", message: "AZUREGERMANCLOUD"},
		{name: "unsupported Stack cloud", cloud: "azureStackCloud", tenant: "test-tenant", message: "AZURESTACKCLOUD"},
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
	configuration := cloud.Configuration{ActiveDirectoryAuthorityHost: "https://example.invalid"}
	client, err := getClient(configuration, "test-subscription", &helperCredential{}, nil)
	require.ErrorContains(t, err, "failed to create resource groups client")
	require.ErrorContains(t, err, "missing Azure Resource Manager configuration")
	assert.Nil(t, client, "factory failure must not return a client")
}

type helperCredential struct {
	scopes []string
}

func (c *helperCredential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.scopes = options.Scopes
	return azcore.AccessToken{Token: "test-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type helperTransport func(*http.Request) (*http.Response, error)

func (transport helperTransport) Do(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestClientUsesSelectedCloud(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint string
		scope    string
	}{
		// ARM's audiences retain their trailing slash before the SDK adds /.default.
		{name: "AzurePublicCloud", endpoint: "https://management.azure.com", scope: "https://management.core.windows.net//.default"},
		{name: "AzureUSGovernmentCloud", endpoint: "https://management.usgovcloudapi.net", scope: "https://management.core.usgovcloudapi.net//.default"},
		{name: "AzureChinaCloud", endpoint: "https://management.chinacloudapi.cn", scope: "https://management.core.chinacloudapi.cn//.default"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration, err := cloudConfigurationFromName(test.name)
			require.NoError(t, err)
			credential := &helperCredential{}
			transport := helperTransport(func(request *http.Request) (*http.Response, error) {
				assert.Equal(t, test.endpoint, request.URL.Scheme+"://"+request.URL.Host)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"name":"test-group","location":"test-region"}`)),
					Request:    request,
				}, nil
			})
			client, err := getClient(configuration, "test-subscription", credential,
				&arm.ClientOptions{ClientOptions: azcore.ClientOptions{Transport: transport}})
			require.NoError(t, err)

			_, err = client.groupsClient.Get(t.Context(), "test-group", nil)

			require.NoError(t, err)
			assert.Equal(t, []string{test.scope}, credential.scopes)
		})
	}
}

func TestDeleteResourceGroup(t *testing.T) {
	const groupPath = "/subscriptions/test-subscription/resourcegroups/test-group"
	const operationPath = "/operations/delete-test-group"
	type response struct {
		method, path, body string
		status             int
		location           string
	}
	found := response{method: http.MethodGet, path: groupPath, status: http.StatusOK, body: `{"name":"test-group"}`}
	accepted := response{
		method: http.MethodDelete, path: groupPath, status: http.StatusAccepted,
		location: "https://management.azure.com" + operationPath,
	}
	for _, test := range []struct {
		name        string
		responses   []response
		message     string
		serviceCode string
	}{
		{
			name: "completed deletion",
			responses: []response{
				found, accepted,
				{method: http.MethodGet, path: operationPath, status: http.StatusNoContent},
			},
		},
		{
			name: "resource group already absent",
			responses: []response{
				{method: http.MethodGet, path: groupPath, status: http.StatusNotFound, body: `{"error":{"code":"ResourceGroupNotFound"}}`},
			},
		},
		{
			name: "lookup permission failure",
			responses: []response{
				{method: http.MethodGet, path: groupPath, status: http.StatusForbidden, body: `{"error":{"code":"AuthorizationFailed"}}`},
			},
			message: "cannot check resource group", serviceCode: "AuthorizationFailed",
		},
		{
			name: "missing subscription is not an absent group",
			responses: []response{
				{method: http.MethodGet, path: groupPath, status: http.StatusNotFound, body: `{"error":{"code":"SubscriptionNotFound"}}`},
			},
			message: "cannot check resource group", serviceCode: "SubscriptionNotFound",
		},
		{
			name: "delete submission fails",
			responses: []response{
				found,
				{method: http.MethodDelete, path: groupPath, status: http.StatusForbidden, body: `{"error":{"code":"AuthorizationFailed"}}`},
			},
			message: "cannot delete resource group", serviceCode: "AuthorizationFailed",
		},
		{
			name: "polling fails after deletion was accepted",
			responses: []response{
				found, accepted,
				{method: http.MethodGet, path: operationPath, status: http.StatusForbidden, body: `{"error":{"code":"AuthorizationFailed"}}`},
			},
			message: "cannot confirm deletion of resource group", serviceCode: "AuthorizationFailed",
		},
		{
			name: "missing polling operation does not prove group deletion",
			responses: []response{
				found, accepted,
				{method: http.MethodGet, path: operationPath, status: http.StatusNotFound, body: `{"error":{"code":"OperationNotFound"}}`},
			},
			message: "cannot confirm deletion of resource group", serviceCode: "OperationNotFound",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			transport := helperTransport(func(request *http.Request) (*http.Response, error) {
				require.Less(t, requests, len(test.responses), "unexpected request: %s %s", request.Method, request.URL)
				step := test.responses[requests]
				requests++
				assert.Equal(t, step.method, request.Method)
				assert.Equal(t, step.path, request.URL.Path)
				headers := http.Header{"Content-Type": []string{"application/json"}}
				if step.location != "" {
					headers.Set("Location", step.location)
				}
				return &http.Response{
					StatusCode: step.status, Header: headers, Body: io.NopCloser(strings.NewReader(step.body)), Request: request,
				}, nil
			})
			configuration, err := cloudConfigurationFromName("AzurePublicCloud")
			require.NoError(t, err)
			client, err := getClient(configuration, "test-subscription", &helperCredential{},
				&arm.ClientOptions{ClientOptions: azcore.ClientOptions{
					Transport: transport, Retry: policy.RetryOptions{MaxRetries: -1},
				}})
			require.NoError(t, err)

			err = client.DeleteResourceGroup(t.Context(), "test-group")

			assert.Equal(t, len(test.responses), requests)
			if test.message == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.message)
			require.ErrorContains(t, err, "test-group")
			var responseError *azcore.ResponseError
			require.ErrorAs(t, err, &responseError)
			assert.Equal(t, test.serviceCode, responseError.ErrorCode)
		})
	}
}

func TestDeleteResourceGroupPollingCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "caller cancellation"
		if deadline {
			name = "caller deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var ctx context.Context
				var cancel context.CancelFunc
				expectedErr := context.Canceled
				if deadline {
					ctx, cancel = context.WithTimeout(t.Context(), time.Second)
					expectedErr = context.DeadlineExceeded
				} else {
					ctx, cancel = context.WithCancel(t.Context())
				}
				defer cancel()
				polling := make(chan struct{})
				transport := helperTransport(func(request *http.Request) (*http.Response, error) {
					if request.URL.Path == "/operations/delete-test-group" {
						close(polling)
						<-request.Context().Done()
						return nil, request.Context().Err()
					}
					if request.Method == http.MethodDelete {
						return &http.Response{
							StatusCode: http.StatusAccepted,
							Header:     http.Header{"Location": []string{"https://management.azure.com/operations/delete-test-group"}},
							Body:       http.NoBody, Request: request,
						}, nil
					}
					assert.Equal(t, http.MethodGet, request.Method)
					return &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
						Body: io.NopCloser(strings.NewReader(`{"name":"test-group"}`)), Request: request,
					}, nil
				})
				configuration, err := cloudConfigurationFromName("AzurePublicCloud")
				require.NoError(t, err)
				client, err := getClient(configuration, "test-subscription", &helperCredential{},
					&arm.ClientOptions{ClientOptions: azcore.ClientOptions{Transport: transport}})
				require.NoError(t, err)
				if !deadline {
					go func() {
						<-polling
						cancel()
					}()
				}

				err = client.DeleteResourceGroup(ctx, "test-group")

				require.ErrorIs(t, err, expectedErr)
				require.ErrorContains(t, err, "cannot confirm deletion of resource group")
			})
		})
	}
}

func TestCloudConfigurationsAreIndependent(t *testing.T) {
	public, err := cloudConfigurationFromName("AzurePublicCloud")
	require.NoError(t, err)
	expected := public.Services[cloud.ResourceManager]
	public.Services[cloud.ResourceManager] = cloud.ServiceConfiguration{
		Endpoint: "https://another-arm.invalid", Audience: "https://another-audience.invalid/",
	}

	anotherPublic, err := cloudConfigurationFromName("AzurePublicCloud")

	require.NoError(t, err)
	assert.Equal(t, expected, anotherPublic.Services[cloud.ResourceManager],
		"one client's configuration must not modify shared SDK defaults")
}
