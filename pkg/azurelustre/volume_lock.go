/*
Copyright 2021 The Kubernetes Authors.

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
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	volumeOperationAlreadyExistsFmt = "An operation with the given Volume ID %s already exists"
)

func amlFilesystemLockKey(subscriptionID, resourceGroupName, amlFilesystemName string) string {
	return strings.ToLower(fmt.Sprintf("amlfs/%s/%s/%s", subscriptionID, resourceGroupName, amlFilesystemName))
}

// volumeLocks tracks operation keys in use by this process.
type volumeLocks struct {
	locks sets.Set[string]
	mux   sync.Mutex
}

func newVolumeLocks() *volumeLocks {
	return &volumeLocks{
		locks: sets.New[string](),
	}
}

// TryAcquire returns false if another operation already holds operationKey.
func (vl *volumeLocks) TryAcquire(operationKey string) bool {
	vl.mux.Lock()
	defer vl.mux.Unlock()
	if vl.locks.Has(operationKey) {
		return false
	}
	vl.locks.Insert(operationKey)
	return true
}

func (vl *volumeLocks) Release(operationKey string) {
	vl.mux.Lock()
	defer vl.mux.Unlock()
	vl.locks.Delete(operationKey)
}
