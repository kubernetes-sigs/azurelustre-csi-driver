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
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/go-autorest/autorest/azure"
)

type Client struct {
	environment    azure.Environment
	subscriptionID string
	groupsClient   *armresources.ResourceGroupsClient
}

func GetClient(cloud string, subscriptionID string, clientID string, tenantID string, clientSecret string) (*Client, error) {
	env, err := azure.EnvironmentFromName(cloud)
	if err != nil {
		return nil, err
	}
	credential, err := azidentity.NewClientSecretCredential(tenantID, clientID, clientSecret, nil)
	if err != nil {
		log.Fatal(err)
	}
	return getClient(env, subscriptionID, credential), nil
}

func (az *Client) EnsureResourceGroup(ctx context.Context, name, location string, managedBy *string) (resourceGroup *armresources.ResourceGroup, err error) {
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
	tags["buildID"] = stringPointer(os.Getenv("BUILD_ID"))
	tags["jobName"] = stringPointer(os.Getenv("JOB_NAME"))
	tags["creationTimestamp"] = stringPointer(time.Now().UTC().Format(time.RFC3339))

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
	if err == nil {
		pollerResp, err := az.groupsClient.BeginDelete(ctx, groupName, nil)
		if err != nil {
			return fmt.Errorf("cannot delete resource group %v: %w", groupName, err)
		}
		_, err = pollerResp.PollUntilDone(ctx, nil)
		if err != nil {
			// Skip the teardown errors because of https://github.com/Azure/go-autorest/issues/357
			// TODO(feiskyer): fix the issue by upgrading go-autorest version >= v11.3.2.
			log.Printf("Warning: failed to delete resource group %q with error %v", groupName, err)
		}
	}
	return nil
}

func getClient(env azure.Environment, subscriptionID string, credential *azidentity.ClientSecretCredential) *Client {
	groupsClientFactory, err := armresources.NewClientFactory(subscriptionID, credential, nil)
	if err != nil {
		log.Fatal(err)
	}
	c := &Client{
		environment:    env,
		subscriptionID: subscriptionID,
		groupsClient:   groupsClientFactory.NewResourceGroupsClient(),
	}

	return c
}

func stringPointer(s string) *string {
	return &s
}
