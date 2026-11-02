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
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	persistentVolumePageLimit int64 = 500
)

var (
	preDeleteListBackoff = wait.Backoff{
		Duration: 2 * time.Second,
		Factor:   1,
		Steps:    6,
	}
	preDeleteKubeClientFactory = getKubeClient
)

type persistentVolumeLister interface {
	List(ctx context.Context, options metav1.ListOptions) (*corev1.PersistentVolumeList, error)
}

// CheckPersistentVolumesBeforeUninstall blocks uninstall while this driver owns PersistentVolumes.
func CheckPersistentVolumesBeforeUninstall(ctx context.Context, driverName string) error {
	kubeClient, err := preDeleteKubeClientFactory()
	if err != nil {
		return fmt.Errorf("create in-cluster Kubernetes client: %w", err)
	}

	return checkPersistentVolumesBeforeUninstall(
		ctx,
		kubeClient.CoreV1().PersistentVolumes(),
		driverName,
		preDeleteListBackoff,
	)
}

func checkPersistentVolumesBeforeUninstall(
	ctx context.Context,
	lister persistentVolumeLister,
	driverName string,
	backoff wait.Backoff,
) error {
	if strings.TrimSpace(driverName) == "" {
		return errors.New("CSI driver name must not be empty")
	}

	var persistentVolumeNames []string
	var listErr error
	retryErr := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		persistentVolumeNames, listErr = listPersistentVolumesForDriver(ctx, lister, driverName)
		return listErr == nil, nil
	})
	if retryErr != nil {
		if wait.Interrupted(retryErr) && ctx.Err() == nil && listErr != nil {
			return fmt.Errorf("list PersistentVolumes after %d attempts: %w", backoff.Steps, listErr)
		}
		return fmt.Errorf("list PersistentVolumes: %w", retryErr)
	}

	if len(persistentVolumeNames) > 0 {
		return fmt.Errorf(
			"found %d PersistentVolume(s) still using CSI driver %q: %s",
			len(persistentVolumeNames),
			driverName,
			strings.Join(persistentVolumeNames, ", "),
		)
	}

	return nil
}

func listPersistentVolumesForDriver(
	ctx context.Context,
	lister persistentVolumeLister,
	driverName string,
) ([]string, error) {
	options := metav1.ListOptions{Limit: persistentVolumePageLimit}
	persistentVolumeNames := make([]string, 0)

	for {
		page, err := lister.List(ctx, options)
		if err != nil {
			return nil, err
		}

		for index := range page.Items {
			persistentVolume := &page.Items[index]
			if persistentVolume.Spec.CSI != nil && persistentVolume.Spec.CSI.Driver == driverName {
				persistentVolumeNames = append(persistentVolumeNames, persistentVolume.Name)
			}
		}

		if page.Continue == "" {
			break
		}
		options.Continue = page.Continue
	}

	sort.Strings(persistentVolumeNames)
	return persistentVolumeNames, nil
}
