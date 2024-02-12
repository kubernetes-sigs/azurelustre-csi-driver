// /*
// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

// Code generated by client-gen. DO NOT EDIT.
package subnetclient

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/tracing"
	armnetwork "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v4"

	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/metrics"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/utils"
)

type Client struct {
	*armnetwork.SubnetsClient
	subscriptionID string
	tracer         tracing.Tracer
}

func New(subscriptionID string, credential azcore.TokenCredential, options *arm.ClientOptions) (Interface, error) {
	if options == nil {
		options = utils.GetDefaultOption()
	}
	tr := options.TracingProvider.NewTracer(utils.ModuleName, utils.ModuleVersion)

	client, err := armnetwork.NewSubnetsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	return &Client{
		SubnetsClient:  client,
		subscriptionID: subscriptionID,
		tracer:         tr,
	}, nil
}

const GetOperationName = "SubnetsClient.Get"

// Get gets the Subnet
func (client *Client) Get(ctx context.Context, resourceGroupName string, parentResourceName string, resourceName string, expand *string) (result *armnetwork.Subnet, err error) {
	var ops *armnetwork.SubnetsClientGetOptions
	if expand != nil {
		ops = &armnetwork.SubnetsClientGetOptions{Expand: expand}
	}
	metricsCtx := metrics.BeginARMRequest(client.subscriptionID, resourceGroupName, "Subnet", "get")
	defer func() { metricsCtx.Observe(ctx, err) }()
	ctx, endSpan := runtime.StartSpan(ctx, GetOperationName, client.tracer, nil)
	defer endSpan(err)
	resp, err := client.SubnetsClient.Get(ctx, resourceGroupName, parentResourceName, resourceName, ops)
	if err != nil {
		return nil, err
	}
	//handle statuscode
	return &resp.Subnet, nil
}

const CreateOrUpdateOperationName = "SubnetsClient.Create"

// CreateOrUpdate creates or updates a Subnet.
func (client *Client) CreateOrUpdate(ctx context.Context, resourceGroupName string, resourceName string, parentResourceName string, resource armnetwork.Subnet) (result *armnetwork.Subnet, err error) {
	metricsCtx := metrics.BeginARMRequest(client.subscriptionID, resourceGroupName, "Subnet", "create_or_update")
	defer func() { metricsCtx.Observe(ctx, err) }()
	ctx, endSpan := runtime.StartSpan(ctx, CreateOrUpdateOperationName, client.tracer, nil)
	defer endSpan(err)
	resp, err := utils.NewPollerWrapper(client.SubnetsClient.BeginCreateOrUpdate(ctx, resourceGroupName, resourceName, parentResourceName, resource, nil)).WaitforPollerResp(ctx)
	if err != nil {
		return nil, err
	}
	if resp != nil {
		return &resp.Subnet, nil
	}
	return nil, nil
}

const DeleteOperationName = "SubnetsClient.Delete"

// Delete deletes a Subnet by name.
func (client *Client) Delete(ctx context.Context, resourceGroupName string, parentResourceName string, resourceName string) (err error) {
	metricsCtx := metrics.BeginARMRequest(client.subscriptionID, resourceGroupName, "Subnet", "delete")
	defer func() { metricsCtx.Observe(ctx, err) }()
	ctx, endSpan := runtime.StartSpan(ctx, DeleteOperationName, client.tracer, nil)
	defer endSpan(err)
	_, err = utils.NewPollerWrapper(client.BeginDelete(ctx, resourceGroupName, parentResourceName, resourceName, nil)).WaitforPollerResp(ctx)
	return err
}

const ListOperationName = "SubnetsClient.List"

// List gets a list of Subnet in the resource group.
func (client *Client) List(ctx context.Context, resourceGroupName string, parentResourceName string) (result []*armnetwork.Subnet, err error) {
	metricsCtx := metrics.BeginARMRequest(client.subscriptionID, resourceGroupName, "Subnet", "list")
	defer func() { metricsCtx.Observe(ctx, err) }()
	ctx, endSpan := runtime.StartSpan(ctx, ListOperationName, client.tracer, nil)
	defer endSpan(err)
	pager := client.SubnetsClient.NewListPager(resourceGroupName, parentResourceName, nil)
	for pager.More() {
		nextResult, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, nextResult.Value...)
	}
	return result, nil
}
