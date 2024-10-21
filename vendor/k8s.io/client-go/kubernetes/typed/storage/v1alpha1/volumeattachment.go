/*
Copyright The Kubernetes Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package v1alpha1

import (
	"context"

	v1alpha1 "k8s.io/api/storage/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	storagev1alpha1 "k8s.io/client-go/applyconfigurations/storage/v1alpha1"
	gentype "k8s.io/client-go/gentype"
	scheme "k8s.io/client-go/kubernetes/scheme"
)

// VolumeAttachmentsGetter has a method to return a VolumeAttachmentInterface.
// A group's client should implement this interface.
type VolumeAttachmentsGetter interface {
	VolumeAttachments() VolumeAttachmentInterface
}

// VolumeAttachmentInterface has methods to work with VolumeAttachment resources.
type VolumeAttachmentInterface interface {
	Create(ctx context.Context, volumeAttachment *v1alpha1.VolumeAttachment, opts v1.CreateOptions) (*v1alpha1.VolumeAttachment, error)
	Update(ctx context.Context, volumeAttachment *v1alpha1.VolumeAttachment, opts v1.UpdateOptions) (*v1alpha1.VolumeAttachment, error)
	// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
	UpdateStatus(ctx context.Context, volumeAttachment *v1alpha1.VolumeAttachment, opts v1.UpdateOptions) (*v1alpha1.VolumeAttachment, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*v1alpha1.VolumeAttachment, error)
	List(ctx context.Context, opts v1.ListOptions) (*v1alpha1.VolumeAttachmentList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.VolumeAttachment, err error)
	Apply(ctx context.Context, volumeAttachment *storagev1alpha1.VolumeAttachmentApplyConfiguration, opts v1.ApplyOptions) (result *v1alpha1.VolumeAttachment, err error)
	// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
	ApplyStatus(ctx context.Context, volumeAttachment *storagev1alpha1.VolumeAttachmentApplyConfiguration, opts v1.ApplyOptions) (result *v1alpha1.VolumeAttachment, err error)
	VolumeAttachmentExpansion
}

// volumeAttachments implements VolumeAttachmentInterface
type volumeAttachments struct {
	*gentype.ClientWithListAndApply[*v1alpha1.VolumeAttachment, *v1alpha1.VolumeAttachmentList, *storagev1alpha1.VolumeAttachmentApplyConfiguration]
}

// newVolumeAttachments returns a VolumeAttachments
func newVolumeAttachments(c *StorageV1alpha1Client) *volumeAttachments {
	return &volumeAttachments{
		gentype.NewClientWithListAndApply[*v1alpha1.VolumeAttachment, *v1alpha1.VolumeAttachmentList, *storagev1alpha1.VolumeAttachmentApplyConfiguration](
			"volumeattachments",
			c.RESTClient(),
			scheme.ParameterCodec,
			"",
			func() *v1alpha1.VolumeAttachment { return &v1alpha1.VolumeAttachment{} },
			func() *v1alpha1.VolumeAttachmentList { return &v1alpha1.VolumeAttachmentList{} }),
	}
}
