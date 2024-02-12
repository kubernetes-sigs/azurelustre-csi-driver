//go:build go1.18
// +build go1.18

// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See License.txt in the project root for license information.
// Code generated by Microsoft (R) AutoRest Code Generator. DO NOT EDIT.
// Changes may cause incorrect behavior and will be lost if the code is regenerated.

package armauthorization

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
)

// ClientFactory is a client factory used to create any client in this module.
// Don't use this type directly, use NewClientFactory instead.
type ClientFactory struct {
	subscriptionID string
	credential     azcore.TokenCredential
	options        *arm.ClientOptions
}

// NewClientFactory creates a new instance of ClientFactory with the specified values.
// The parameter values will be propagated to any client created from this factory.
//   - subscriptionID - The ID of the target subscription.
//   - credential - used to authorize requests. Usually a credential from azidentity.
//   - options - pass nil to accept the default values.
func NewClientFactory(subscriptionID string, credential azcore.TokenCredential, options *arm.ClientOptions) (*ClientFactory, error) {
	_, err := arm.NewClient(moduleName, moduleVersion, credential, options)
	if err != nil {
		return nil, err
	}
	return &ClientFactory{
		subscriptionID: subscriptionID, credential: credential,
		options: options.Clone(),
	}, nil
}

// NewClassicAdministratorsClient creates a new instance of ClassicAdministratorsClient.
func (c *ClientFactory) NewClassicAdministratorsClient() *ClassicAdministratorsClient {
	subClient, _ := NewClassicAdministratorsClient(c.subscriptionID, c.credential, c.options)
	return subClient
}

// NewDenyAssignmentsClient creates a new instance of DenyAssignmentsClient.
func (c *ClientFactory) NewDenyAssignmentsClient() *DenyAssignmentsClient {
	subClient, _ := NewDenyAssignmentsClient(c.subscriptionID, c.credential, c.options)
	return subClient
}

// NewEligibleChildResourcesClient creates a new instance of EligibleChildResourcesClient.
func (c *ClientFactory) NewEligibleChildResourcesClient() *EligibleChildResourcesClient {
	subClient, _ := NewEligibleChildResourcesClient(c.credential, c.options)
	return subClient
}

// NewGlobalAdministratorClient creates a new instance of GlobalAdministratorClient.
func (c *ClientFactory) NewGlobalAdministratorClient() *GlobalAdministratorClient {
	subClient, _ := NewGlobalAdministratorClient(c.credential, c.options)
	return subClient
}

// NewPermissionsClient creates a new instance of PermissionsClient.
func (c *ClientFactory) NewPermissionsClient() *PermissionsClient {
	subClient, _ := NewPermissionsClient(c.subscriptionID, c.credential, c.options)
	return subClient
}

// NewProviderOperationsMetadataClient creates a new instance of ProviderOperationsMetadataClient.
func (c *ClientFactory) NewProviderOperationsMetadataClient() *ProviderOperationsMetadataClient {
	subClient, _ := NewProviderOperationsMetadataClient(c.credential, c.options)
	return subClient
}

// NewRoleAssignmentScheduleInstancesClient creates a new instance of RoleAssignmentScheduleInstancesClient.
func (c *ClientFactory) NewRoleAssignmentScheduleInstancesClient() *RoleAssignmentScheduleInstancesClient {
	subClient, _ := NewRoleAssignmentScheduleInstancesClient(c.credential, c.options)
	return subClient
}

// NewRoleAssignmentScheduleRequestsClient creates a new instance of RoleAssignmentScheduleRequestsClient.
func (c *ClientFactory) NewRoleAssignmentScheduleRequestsClient() *RoleAssignmentScheduleRequestsClient {
	subClient, _ := NewRoleAssignmentScheduleRequestsClient(c.credential, c.options)
	return subClient
}

// NewRoleAssignmentSchedulesClient creates a new instance of RoleAssignmentSchedulesClient.
func (c *ClientFactory) NewRoleAssignmentSchedulesClient() *RoleAssignmentSchedulesClient {
	subClient, _ := NewRoleAssignmentSchedulesClient(c.credential, c.options)
	return subClient
}

// NewRoleAssignmentsClient creates a new instance of RoleAssignmentsClient.
func (c *ClientFactory) NewRoleAssignmentsClient() *RoleAssignmentsClient {
	subClient, _ := NewRoleAssignmentsClient(c.subscriptionID, c.credential, c.options)
	return subClient
}

// NewRoleDefinitionsClient creates a new instance of RoleDefinitionsClient.
func (c *ClientFactory) NewRoleDefinitionsClient() *RoleDefinitionsClient {
	subClient, _ := NewRoleDefinitionsClient(c.credential, c.options)
	return subClient
}

// NewRoleEligibilityScheduleInstancesClient creates a new instance of RoleEligibilityScheduleInstancesClient.
func (c *ClientFactory) NewRoleEligibilityScheduleInstancesClient() *RoleEligibilityScheduleInstancesClient {
	subClient, _ := NewRoleEligibilityScheduleInstancesClient(c.credential, c.options)
	return subClient
}

// NewRoleEligibilityScheduleRequestsClient creates a new instance of RoleEligibilityScheduleRequestsClient.
func (c *ClientFactory) NewRoleEligibilityScheduleRequestsClient() *RoleEligibilityScheduleRequestsClient {
	subClient, _ := NewRoleEligibilityScheduleRequestsClient(c.credential, c.options)
	return subClient
}

// NewRoleEligibilitySchedulesClient creates a new instance of RoleEligibilitySchedulesClient.
func (c *ClientFactory) NewRoleEligibilitySchedulesClient() *RoleEligibilitySchedulesClient {
	subClient, _ := NewRoleEligibilitySchedulesClient(c.credential, c.options)
	return subClient
}

// NewRoleManagementPoliciesClient creates a new instance of RoleManagementPoliciesClient.
func (c *ClientFactory) NewRoleManagementPoliciesClient() *RoleManagementPoliciesClient {
	subClient, _ := NewRoleManagementPoliciesClient(c.credential, c.options)
	return subClient
}

// NewRoleManagementPolicyAssignmentsClient creates a new instance of RoleManagementPolicyAssignmentsClient.
func (c *ClientFactory) NewRoleManagementPolicyAssignmentsClient() *RoleManagementPolicyAssignmentsClient {
	subClient, _ := NewRoleManagementPolicyAssignmentsClient(c.credential, c.options)
	return subClient
}
