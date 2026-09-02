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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const (
	preDeleteTestDriverName = "azurelustre.csi.azure.com"
)

type persistentVolumeListResponse struct {
	list *corev1.PersistentVolumeList
	err  error
}

type fakePersistentVolumeLister struct {
	responses []persistentVolumeListResponse
	options   []metav1.ListOptions
}

func (f *fakePersistentVolumeLister) List(
	_ context.Context,
	options metav1.ListOptions,
) (*corev1.PersistentVolumeList, error) {
	f.options = append(f.options, options)
	responseIndex := len(f.options) - 1
	if responseIndex >= len(f.responses) {
		return nil, errors.New("unexpected PersistentVolume list call")
	}
	return f.responses[responseIndex].list, f.responses[responseIndex].err
}

func csiPersistentVolume(name, driver string) corev1.PersistentVolume {
	return corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{Driver: driver},
		}},
	}
}

func flexPersistentVolume(name, driver string) corev1.PersistentVolume {
	return corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
			FlexVolume: &corev1.FlexPersistentVolumeSource{Driver: driver},
		}},
	}
}

func TestCheckPersistentVolumesBeforeUninstall(t *testing.T) {
	// Arrange
	persistentVolume := csiPersistentVolume("matching-csi", preDeleteTestDriverName)
	originalFactory := preDeleteKubeClientFactory
	t.Cleanup(func() { preDeleteKubeClientFactory = originalFactory })
	preDeleteKubeClientFactory = func() (kubernetes.Interface, error) {
		return kubefake.NewSimpleClientset(&persistentVolume), nil
	}

	// Act
	err := CheckPersistentVolumesBeforeUninstall(t.Context(), preDeleteTestDriverName)

	// Assert
	require.EqualError(
		t,
		err,
		"found 1 PersistentVolume(s) still using CSI driver \"azurelustre.csi.azure.com\": matching-csi",
	)
}

func TestCheckPersistentVolumesBeforeUninstallReturnsClientError(t *testing.T) {
	// Arrange
	originalFactory := preDeleteKubeClientFactory
	t.Cleanup(func() { preDeleteKubeClientFactory = originalFactory })
	preDeleteKubeClientFactory = func() (kubernetes.Interface, error) {
		return nil, errors.New("client failed")
	}

	// Act
	err := CheckPersistentVolumesBeforeUninstall(t.Context(), preDeleteTestDriverName)

	// Assert
	require.EqualError(t, err, "create in-cluster Kubernetes client: client failed")
}

func TestCheckPersistentVolumesBeforeUninstallAllowsEmptyList(t *testing.T) {
	// Arrange
	lister := &fakePersistentVolumeLister{responses: []persistentVolumeListResponse{{
		list: &corev1.PersistentVolumeList{},
	}}}

	// Act
	err := checkPersistentVolumesBeforeUninstall(
		t.Context(), lister, preDeleteTestDriverName, wait.Backoff{Steps: 1},
	)

	// Assert
	require.NoError(t, err)
	assert.Len(t, lister.options, 1)
}

func TestCheckPersistentVolumesBeforeUninstallFiltersSources(t *testing.T) {
	// Arrange
	lister := &fakePersistentVolumeLister{responses: []persistentVolumeListResponse{{
		list: &corev1.PersistentVolumeList{Items: []corev1.PersistentVolume{
			csiPersistentVolume("matching-csi", preDeleteTestDriverName),
			csiPersistentVolume("other-csi", "disk.csi.azure.com"),
			flexPersistentVolume("same-name-flex-volume", preDeleteTestDriverName),
		}},
	}}}

	// Act
	err := checkPersistentVolumesBeforeUninstall(
		t.Context(), lister, preDeleteTestDriverName, wait.Backoff{Steps: 1},
	)

	// Assert
	require.EqualError(
		t,
		err,
		"found 1 PersistentVolume(s) still using CSI driver \"azurelustre.csi.azure.com\": matching-csi",
	)
	assert.Len(t, lister.options, 1)
}

func TestCheckPersistentVolumesBeforeUninstallRejectsEmptyDriver(t *testing.T) {
	// Arrange
	lister := &fakePersistentVolumeLister{}

	// Act
	err := checkPersistentVolumesBeforeUninstall(t.Context(), lister, " ", wait.Backoff{Steps: 1})

	// Assert
	require.EqualError(t, err, "CSI driver name must not be empty")
	assert.Empty(t, lister.options)
}

func TestCheckPersistentVolumesBeforeUninstallRetries(t *testing.T) {
	// Arrange
	lister := &fakePersistentVolumeLister{responses: []persistentVolumeListResponse{
		{err: errors.New("temporary API failure")},
		{list: &corev1.PersistentVolumeList{}},
	}}

	// Act
	err := checkPersistentVolumesBeforeUninstall(
		t.Context(), lister, preDeleteTestDriverName, wait.Backoff{Steps: 2},
	)

	// Assert
	require.NoError(t, err)
	assert.Len(t, lister.options, 2)
}

func TestCheckPersistentVolumesBeforeUninstallFailsClosed(t *testing.T) {
	// Arrange
	lister := &fakePersistentVolumeLister{responses: []persistentVolumeListResponse{
		{err: errors.New("persistent API failure")},
		{err: errors.New("persistent API failure")},
	}}

	// Act
	err := checkPersistentVolumesBeforeUninstall(
		t.Context(), lister, preDeleteTestDriverName, wait.Backoff{Steps: 2},
	)

	// Assert
	require.EqualError(t, err, "list PersistentVolumes after 2 attempts: persistent API failure")
	assert.Len(t, lister.options, 2)
}

func TestCheckPersistentVolumesBeforeUninstallPaginates(t *testing.T) {
	// Arrange
	lister := &fakePersistentVolumeLister{responses: []persistentVolumeListResponse{
		{
			list: &corev1.PersistentVolumeList{
				ListMeta: metav1.ListMeta{Continue: "second-page"},
				Items:    []corev1.PersistentVolume{csiPersistentVolume("volume-b", preDeleteTestDriverName)},
			},
		},
		{
			list: &corev1.PersistentVolumeList{
				Items: []corev1.PersistentVolume{csiPersistentVolume("volume-a", preDeleteTestDriverName)},
			},
		},
	}}
	backoff := wait.Backoff{Steps: 1}

	// Act
	err := checkPersistentVolumesBeforeUninstall(
		t.Context(),
		lister,
		preDeleteTestDriverName,
		backoff,
	)

	// Assert
	require.EqualError(
		t,
		err,
		"found 2 PersistentVolume(s) still using CSI driver \"azurelustre.csi.azure.com\": volume-a, volume-b",
	)
	require.Len(t, lister.options, 2)
	assert.Equal(t, int64(500), lister.options[0].Limit)
	assert.Empty(t, lister.options[0].Continue)
	assert.Equal(t, "second-page", lister.options[1].Continue)
}

func TestCheckPersistentVolumesBeforeUninstallHonorsCancellation(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	lister := &fakePersistentVolumeLister{}
	backoff := wait.Backoff{Steps: 2}

	// Act
	err := checkPersistentVolumesBeforeUninstall(ctx, lister, preDeleteTestDriverName, backoff)

	// Assert
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, lister.options)
}

func TestCheckPersistentVolumesBeforeUninstallFailsClosedOnLaterPage(t *testing.T) {
	lister := &fakePersistentVolumeLister{responses: []persistentVolumeListResponse{
		{list: &corev1.PersistentVolumeList{ListMeta: metav1.ListMeta{Continue: "second-page"}}},
		{err: errors.New("later page unavailable")},
	}}

	err := checkPersistentVolumesBeforeUninstall(
		t.Context(), lister, preDeleteTestDriverName, wait.Backoff{Steps: 1},
	)

	require.EqualError(t, err, "list PersistentVolumes after 1 attempts: later page unavailable")
	require.Len(t, lister.options, 2)
	assert.Equal(t, "second-page", lister.options[1].Continue)
}

func TestCheckPersistentVolumesBeforeUninstallRestartsPaginationOnRetry(t *testing.T) {
	lister := &fakePersistentVolumeLister{responses: []persistentVolumeListResponse{
		{list: &corev1.PersistentVolumeList{
			ListMeta: metav1.ListMeta{Continue: "expired-page"},
			Items:    []corev1.PersistentVolume{csiPersistentVolume("deleted-volume", preDeleteTestDriverName)},
		}},
		{err: errors.New("continuation expired")},
		{list: &corev1.PersistentVolumeList{ListMeta: metav1.ListMeta{Continue: "fresh-page"}}},
		{list: &corev1.PersistentVolumeList{
			Items: []corev1.PersistentVolume{csiPersistentVolume("remaining-volume", preDeleteTestDriverName)},
		}},
	}}

	err := checkPersistentVolumesBeforeUninstall(
		t.Context(), lister, preDeleteTestDriverName, wait.Backoff{Steps: 2},
	)

	require.EqualError(t, err,
		"found 1 PersistentVolume(s) still using CSI driver \"azurelustre.csi.azure.com\": remaining-volume")
	require.Len(t, lister.options, 4)
	assert.Equal(t, "expired-page", lister.options[1].Continue)
	assert.Empty(t, lister.options[2].Continue, "retry must start a fresh list, not resume the failed snapshot")
	assert.Equal(t, "fresh-page", lister.options[3].Continue)
}

func TestCheckPersistentVolumesBeforeUninstallDeadlineStopsRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	lister := &fakePersistentVolumeLister{responses: []persistentVolumeListResponse{
		{err: errors.New("API unavailable")},
	}}

	err := checkPersistentVolumesBeforeUninstall(
		ctx, lister, preDeleteTestDriverName, wait.Backoff{Steps: 2, Duration: time.Hour},
	)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.LessOrEqual(t, len(lister.options), 1)
}
