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
	"fmt"
	"math"
	"net/netip"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/azurelustre-csi-driver/pkg/util"
)

var errAmlFilesystemFailed = status.Error(codes.Unavailable, "unfinished AMLFS provisioning reports Failed")

func validateAmlFilesystemIdentity(filesystem *armstoragecache.AmlFilesystem, resourceID string) error {
	if filesystem.ID == nil || *filesystem.ID == "" {
		return status.Errorf(codes.Unavailable, "received incomplete AMLFS response for %s: missing resource ID", resourceID)
	}
	if !strings.EqualFold(*filesystem.ID, resourceID) {
		return status.Errorf(codes.Unavailable, "received AMLFS response for %s with unexpected resource ID %q; preserving the filesystem", resourceID, *filesystem.ID)
	}
	return nil
}

func amlFilesystemState(resourceID string, properties *armstoragecache.AmlFilesystemProperties) (armstoragecache.AmlFilesystemProvisioningStateType, error) {
	if properties == nil || properties.ProvisioningState == nil || *properties.ProvisioningState == "" {
		return "", status.Errorf(codes.Unavailable, "received incomplete AMLFS response for %s: missing provisioning state", resourceID)
	}
	switch *properties.ProvisioningState {
	case armstoragecache.AmlFilesystemProvisioningStateTypeSucceeded,
		armstoragecache.AmlFilesystemProvisioningStateTypeCreating,
		armstoragecache.AmlFilesystemProvisioningStateTypeUpdating,
		armstoragecache.AmlFilesystemProvisioningStateTypeDeleting,
		armstoragecache.AmlFilesystemProvisioningStateTypeFailed,
		armstoragecache.AmlFilesystemProvisioningStateTypeCanceled:
		return *properties.ProvisioningState, nil
	default:
		return "", status.Errorf(codes.Unavailable, "received AMLFS response for %s with unknown provisioning state %q; retry observation without modifying the filesystem",
			resourceID, *properties.ProvisioningState)
	}
}

func (d *DynamicProvisioner) amlFilesystemResult(filesystem *armstoragecache.AmlFilesystem, requested *AmlFilesystemProperties) (*AmlFilesystemResult, error) {
	resourceID := amlFilesystemResourceID(d.subscriptionID, requested.ResourceGroupName, requested.AmlFilesystemName)
	if err := validateAmlFilesystemIdentity(filesystem, resourceID); err != nil {
		return nil, err
	}
	state, err := amlFilesystemState(resourceID, filesystem.Properties)
	if err != nil {
		return nil, err
	}
	switch state {
	case armstoragecache.AmlFilesystemProvisioningStateTypeCreating, armstoragecache.AmlFilesystemProvisioningStateTypeUpdating:
		return nil, status.Errorf(codes.Aborted, "AMLFS %s is %s; retry after the ARM operation completes", resourceID, state)
	case armstoragecache.AmlFilesystemProvisioningStateTypeDeleting:
		return nil, status.Errorf(codes.Aborted, "AMLFS %s is Deleting; waiting for deletion to complete before retrying creation", resourceID)
	case armstoragecache.AmlFilesystemProvisioningStateTypeFailed:
		if err := validateAmlFilesystemCompatibility(filesystem, requested, resourceID); err != nil {
			return nil, err
		}
		if filesystem.Properties.StorageCapacityTiB == nil || *filesystem.Properties.StorageCapacityTiB <= 0 ||
			(requested.StorageCapacityTiB != 0 && *filesystem.Properties.StorageCapacityTiB != requested.StorageCapacityTiB) {
			return nil, status.Errorf(codes.Unavailable, "AMLFS %s failed provisioning has missing or mismatched creation capacity; preserving the filesystem", resourceID)
		}
		return nil, fmt.Errorf("AMLFS %s: %w", resourceID, errAmlFilesystemFailed)
	case armstoragecache.AmlFilesystemProvisioningStateTypeCanceled:
		return nil, status.Errorf(codes.Unavailable,
			"AMLFS %s reports %s; preserving the filesystem until ARM reports a supported provisioning outcome",
			resourceID, state)
	case armstoragecache.AmlFilesystemProvisioningStateTypeSucceeded:
		// ARM succeeded; the fields consumed by CSI must also be complete.
	}
	if err := validateAmlFilesystemCompatibility(filesystem, requested, resourceID); err != nil {
		return nil, err
	}

	properties := filesystem.Properties
	if properties.Health == nil || properties.Health.State == nil || *properties.Health.State == "" {
		return nil, status.Errorf(codes.Unavailable, "received incomplete AMLFS response for %s: missing health state", resourceID)
	}
	if *properties.Health.State != armstoragecache.AmlFilesystemHealthStateTypeAvailable {
		return nil, status.Errorf(codes.Unavailable, "AMLFS %s provisioning succeeded but health is %s; waiting for Available without modifying the filesystem",
			resourceID, *properties.Health.State)
	}
	if properties.CurrentStorageCapacityTiB == nil {
		return nil, status.Errorf(codes.Unavailable, "received incomplete AMLFS response for %s: missing current storage capacity", resourceID)
	}
	// StorageCapacityTiB is the original requested size, not capacity after expansion.
	capacity := float64(*properties.CurrentStorageCapacityTiB) * float64(util.TiB)
	if math.IsNaN(capacity) || math.IsInf(capacity, 0) || capacity <= 0 ||
		capacity >= float64(math.MaxInt64) || math.Trunc(capacity) != capacity {
		return nil, status.Errorf(codes.Unavailable, "received invalid current AMLFS capacity for %s: %v TiB", resourceID, *properties.CurrentStorageCapacityTiB)
	}
	capacityBytes := int64(capacity)
	if capacityBytes < requested.RequiredBytes || (requested.LimitBytes != 0 && capacityBytes > requested.LimitBytes) {
		return nil, status.Errorf(codes.AlreadyExists, "AMLFS %s has current capacity %d bytes outside the requested range [%d, %d] (zero limit is unbounded)",
			resourceID, capacityBytes, requested.RequiredBytes, requested.LimitBytes)
	}
	if properties.ClientInfo == nil || properties.ClientInfo.MgsAddress == nil ||
		strings.TrimSpace(*properties.ClientInfo.MgsAddress) == "" {
		return nil, status.Errorf(codes.Unavailable, "received incomplete AMLFS response for %s: missing MGS address", resourceID)
	}
	mgsAddress := *properties.ClientInfo.MgsAddress
	address, err := netip.ParseAddr(mgsAddress)
	if err != nil || !address.Is4() {
		return nil, status.Errorf(codes.Unavailable, "received invalid AMLFS MGS IPv4 address for %s: %q", resourceID, mgsAddress)
	}
	return &AmlFilesystemResult{MGSAddress: mgsAddress, CapacityBytes: capacityBytes}, nil
}

func validateAmlFilesystemCompatibility(filesystem *armstoragecache.AmlFilesystem, requested *AmlFilesystemProperties, resourceID string) error {
	if filesystem.Properties == nil {
		return status.Errorf(codes.Unavailable, "received incomplete AMLFS response for %s: missing properties", resourceID)
	}
	var skuName *string
	if filesystem.SKU != nil {
		skuName = filesystem.SKU.Name
	}
	for _, field := range []struct {
		name      string
		observed  *string
		requested string
	}{
		{name: "location", observed: filesystem.Location, requested: requested.Location},
		{name: "SKU", observed: skuName, requested: requested.SKUName},
		{name: "filesystem subnet", observed: filesystem.Properties.FilesystemSubnet, requested: requested.SubnetInfo.SubnetID},
	} {
		if field.observed == nil || strings.TrimSpace(*field.observed) == "" {
			return status.Errorf(codes.Unavailable, "received incomplete AMLFS response for %s: missing %s", resourceID, field.name)
		}
		if field.requested != "" && !strings.EqualFold(*field.observed, field.requested) {
			return status.Errorf(codes.AlreadyExists, "AMLFS %s has %s %q, incompatible with requested %q",
				resourceID, field.name, *field.observed, field.requested)
		}
	}
	if len(filesystem.Zones) > 1 || (len(filesystem.Zones) == 1 &&
		(filesystem.Zones[0] == nil || strings.TrimSpace(*filesystem.Zones[0]) == "")) {
		return status.Errorf(codes.Unavailable, "received malformed AMLFS zone response for %s", resourceID)
	}
	if requested.Zone != "" {
		if len(filesystem.Zones) == 0 {
			return status.Errorf(codes.Unavailable, "received incomplete AMLFS response for %s: missing requested zone", resourceID)
		}
		if *filesystem.Zones[0] != requested.Zone {
			return status.Errorf(codes.AlreadyExists, "AMLFS %s has zone %q, incompatible with requested %q",
				resourceID, *filesystem.Zones[0], requested.Zone)
		}
	}
	// Tags, maintenance settings and managed identities may have been changed by the customer.
	return nil
}
