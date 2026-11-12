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

package azure

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	azureutils "sigs.k8s.io/cloud-provider-azure/pkg/azclient/utils"
)

type Client struct {
	subscriptionID string
	groupsClient   *armresources.ResourceGroupsClient
}

func GetClient(cloudName, subscriptionID, clientID, tenantID, clientSecret string) (*Client, error) {
	configuration, err := cloudConfigurationFromName(cloudName)
	if err != nil {
		return nil, err
	}
	credential, err := azidentity.NewClientSecretCredential(tenantID, clientID, clientSecret,
		&azidentity.ClientSecretCredentialOptions{ClientOptions: azcore.ClientOptions{Cloud: configuration}})
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure credential: %w", err)
	}
	return getClient(configuration, subscriptionID, credential, nil)
}

func cloudConfigurationFromName(name string) (cloud.Configuration, error) {
	name = strings.ToUpper(name)
	if configuration, ok := azureutils.EnvironmentMapping[name]; ok {
		result := *configuration
		result.Services = maps.Clone(configuration.Services)
		return result, nil
	}
	return cloud.Configuration{}, fmt.Errorf("no Azure cloud environment matching the name %q", name)
}

func (az *Client) EnsureResourceGroup(ctx context.Context, name, location string, managedBy *string) (*armresources.ResourceGroup, error) {
	var tags map[string]*string
	group, err := az.groupsClient.Get(ctx, name, nil)
	if err == nil && group.Tags != nil {
		tags = group.Tags
	} else {
		tags = make(map[string]*string)
	}
	if managedBy == nil {
		managedBy = group.ManagedBy
	}
	// Tags for correlating resource groups with prow jobs on testgrid
	tags["buildID"] = new(os.Getenv("BUILD_ID"))
	tags["jobName"] = new(os.Getenv("JOB_NAME"))
	tags["creationTimestamp"] = new(time.Now().UTC().Format(time.RFC3339))

	response, err := az.groupsClient.CreateOrUpdate(ctx, name, armresources.ResourceGroup{
		Name:      &name,
		Location:  &location,
		ManagedBy: managedBy,
		Tags:      tags,
	}, nil)
	if err != nil {
		return &response.ResourceGroup, err
	}

	return &response.ResourceGroup, nil
}

func (az *Client) DeleteResourceGroup(ctx context.Context, groupName string) error {
	_, err := az.groupsClient.Get(ctx, groupName, nil)
	if err != nil {
		var responseError *azcore.ResponseError
		if errors.As(err, &responseError) && responseError.StatusCode == http.StatusNotFound && responseError.ErrorCode == "ResourceGroupNotFound" {
			return nil
		}
		return fmt.Errorf("cannot check resource group %q before deletion: %w", groupName, err)
	}
	poller, err := az.groupsClient.BeginDelete(ctx, groupName, nil)
	if err != nil {
		return fmt.Errorf("cannot delete resource group %q: %w", groupName, err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("cannot confirm deletion of resource group %q: %w", groupName, err)
	}
	return nil
}

func getClient(configuration cloud.Configuration, subscriptionID string, credential azcore.TokenCredential, options *arm.ClientOptions) (*Client, error) {
	var clientOptions arm.ClientOptions
	if options != nil {
		clientOptions = *options
	}
	clientOptions.Cloud = configuration
	groupsClientFactory, err := armresources.NewClientFactory(subscriptionID, credential, &clientOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource groups client: %w", err)
	}
	c := &Client{
		subscriptionID: subscriptionID,
		groupsClient:   groupsClientFactory.NewResourceGroupsClient(),
	}

	return c, nil
}
