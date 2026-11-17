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
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	nodeStatusMaxAge = 2 * time.Minute
)

func (d *Driver) checkNodeMountAdmission(ctx context.Context) error {
	if !d.mountAdmissionPolicyEnabled {
		return nil
	}
	policy, err := d.readMountAdmissionPolicy()
	if err != nil {
		return mountAdmissionError(d.NodeID, "PolicyUnavailable", err.Error())
	}
	if !policy.MountAdmission.Enforce {
		return nil
	}
	if d.dynamicClient == nil || d.kubeClient == nil || d.podNamespace == "" || d.NodeID == "" || d.podName == "" {
		return mountAdmissionError(d.NodeID, "StatusUnavailable",
			"the node status client or node identity is unavailable")
	}
	object, err := d.dynamicClient.Resource(nodeStatusGVR).Namespace(d.podNamespace).Get(
		ctx, d.NodeID, metav1.GetOptions{})
	if err != nil {
		return mountAdmissionError(d.NodeID, "StatusUnavailable",
			fmt.Sprintf("the AzureLustreNodeStatus resource could not be read: %v", err))
	}
	observedAtValue, ok, err := unstructured.NestedString(object.Object, "status", "observedAt")
	if err != nil || !ok {
		return mountAdmissionError(d.NodeID, "StatusStale", "the status has no valid observation time")
	}
	observedAt, err := time.Parse(time.RFC3339, observedAtValue)
	if err != nil || time.Since(observedAt) > nodeStatusMaxAge || observedAt.After(time.Now().Add(30*time.Second)) {
		return mountAdmissionError(d.NodeID, "StatusStale", "the status observation is stale")
	}
	admission, _, err := unstructured.NestedString(object.Object, "status", "mountAdmission")
	if err != nil {
		return mountAdmissionError(d.NodeID, "StatusInvalid", "the mount-admission status is malformed")
	}
	reason, _, err := unstructured.NestedString(object.Object, "status", "reason")
	if err != nil {
		return mountAdmissionError(d.NodeID, "StatusInvalid", "the mount-admission reason is malformed")
	}
	message, _, err := unstructured.NestedString(object.Object, "status", "message")
	if err != nil {
		return mountAdmissionError(d.NodeID, "StatusInvalid", "the mount-admission message is malformed")
	}
	if admission != "Allowed" {
		if reason == "" {
			reason = "AdmissionDenied"
		}
		if message == "" {
			message = "the node does not satisfy the current compatibility policy"
		}
		return mountAdmissionError(d.NodeID, reason, message)
	}
	fingerprint, found, err := unstructured.NestedString(object.Object, "status", "desired", "policyFingerprint")
	if err != nil || !found || fingerprint == "" || fingerprint != policy.fingerprint {
		return mountAdmissionError(d.NodeID, "PolicyChanged", "the status was not computed from the currently projected policy")
	}
	return d.checkAdmissionEvidence(ctx, object, policy)
}

func (d *Driver) checkAdmissionEvidence(ctx context.Context, object *unstructured.Unstructured, policy *compatibilityPolicy) error {
	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, d.NodeID, metav1.GetOptions{})
	if err != nil {
		return mountAdmissionError(d.NodeID, "IdentityUnavailable", fmt.Sprintf("read current Node identity: %v", err))
	}
	if strings.EqualFold(strings.TrimSpace(node.Annotations[mountAdmissionAnnotation]), "denied") {
		return mountAdmissionError(d.NodeID, "AdministrativeQuiescence", "new mounts are administratively disabled")
	}
	pod, err := d.kubeClient.CoreV1().Pods(d.podNamespace).Get(ctx, d.podName, metav1.GetOptions{})
	if err != nil {
		return mountAdmissionError(d.NodeID, "IdentityUnavailable", fmt.Sprintf("read current Pod identity: %v", err))
	}
	if node.DeletionTimestamp != nil || pod.DeletionTimestamp != nil || pod.Spec.NodeName != node.Name {
		return mountAdmissionError(d.NodeID, "IdentityChanged", "the reporting Node or Pod is being replaced")
	}
	annotations, found, err := unstructured.NestedStringMap(object.Object, "status", "observed")
	if err != nil || !found {
		return mountAdmissionError(d.NodeID, "EvidenceUnavailable", "the status has no valid node facts")
	}
	renewed, err := time.Parse(time.RFC3339Nano, annotations["factRenewTime"])
	if err != nil {
		return mountAdmissionError(d.NodeID, "ReporterStale", "the status has no valid fact heartbeat")
	}
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Annotations: annotations},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr(annotations[nodeFactAnnotationPrefix+"pod-uid"]),
			RenewTime:            &metav1.MicroTime{Time: renewed},
			LeaseDurationSeconds: ptr(nodeFactLeaseDuration),
		},
	}
	if !factMatchesPodAndNode(lease, pod, node) || !factIsFresh(lease, time.Now()) {
		return mountAdmissionError(d.NodeID, "EvidenceChanged", "the status no longer describes the current node runtime")
	}
	// Recheck the active pod flavor's image policies at the mount boundary,
	// rather than trusting an Allowed decision as a global digest allowlist.
	switch classifySecurityCompliance(lease, pod.Labels["flavor"], policy.Images) {
	case "Approved", "NonCurrentApproved":
	case "Withdrawn":
		return mountAdmissionError(d.NodeID, "ImageDigestWithdrawn", "the running images are not approved for the current pod flavor")
	default:
		return mountAdmissionError(d.NodeID, "SecurityEvidenceUnknown", "the current pod flavor has no complete image approval")
	}
	// A recent controller heartbeat cannot validate facts from before a reboot
	// or module replacement. Compare the resident state at the mount boundary.
	for field, path := range map[string]string{
		"boot-id":        "/proc/sys/kernel/random/boot_id",
		"kernel-version": "/proc/sys/kernel/osrelease",
		"loaded-client":  "/sys/module/lustre/version",
	} {
		current := d.readNodeFactFile(path)
		if current == "" || current != annotations[nodeFactAnnotationPrefix+field] {
			return mountAdmissionError(d.NodeID, "EvidenceChanged", fmt.Sprintf("current %s does not match the admitted facts", field))
		}
	}
	return nil
}

func (d *Driver) readMountAdmissionPolicy() (*compatibilityPolicy, error) {
	if d.mountAdmissionReadFile == nil || d.mountAdmissionPolicyPath == "" {
		return nil, fmt.Errorf("the compatibility policy reader is unavailable")
	}
	raw, err := d.mountAdmissionReadFile(d.mountAdmissionPolicyPath)
	if err != nil {
		return nil, fmt.Errorf("read compatibility policy: %w", err)
	}
	policy, err := parseCompatibilityPolicy(raw)
	if err != nil {
		return nil, err
	}
	return policy, nil
}

func mountAdmissionError(nodeName, reason, message string) error {
	return status.Errorf(
		codes.FailedPrecondition,
		"new Azure Lustre mounts are denied on node %q: %s: %s; inspect AzureLustreNodeStatus %q in the driver namespace",
		nodeName,
		reason,
		message,
		nodeName,
	)
}
