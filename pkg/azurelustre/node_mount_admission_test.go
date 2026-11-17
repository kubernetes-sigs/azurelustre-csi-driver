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
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	testEnforcedCompatibilityPolicy = `apiVersion: azurelustre.csi.azure.com/v1alpha1
kind: AzureLustreCompatibilityPolicy
clientSetRevision: 1
cacheTTLSeconds: 3600
mountAdmission:
  enforce: true
images:
  driver:
    jammy:
      desiredDigest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      approvedDigests:
        - sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  loader:
    jammy:
      desiredDigest: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
      approvedDigests:
        - sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
clients:
  jammy:
    desired:
      version: "2.17.0"
      shaSuffix: "current"
    compatible:
      - version: "2.17.0"
        shaSuffix: "current"
bootstrap:
  enabled: false
  expiresAt: ""
  csiImage:
    repository: example.invalid/azurelustre
    tag: test
    digest: ""
`
)

func TestCheckNodeMountAdmission(t *testing.T) {
	namespace := "kube-system"
	nodeName := "node-0"
	node, pod, lease, _, _ := testNodeStatusObjects(namespace)
	policy, err := parseCompatibilityPolicy([]byte(testEnforcedCompatibilityPolicy))
	if err != nil {
		t.Fatal(err)
	}
	newStatus := func(admission, reason string, observedAt time.Time) *unstructured.Unstructured {
		facts := stringMapToInterfaceMap(lease.Annotations)
		facts["factRenewTime"] = time.Now().UTC().Format(time.RFC3339Nano)
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "azurelustre.csi.azure.com/v1alpha1",
			"kind":       "AzureLustreNodeStatus",
			"metadata": map[string]interface{}{
				"name":      nodeName,
				"namespace": namespace,
			},
			"status": map[string]interface{}{
				"observedAt":     observedAt.UTC().Format(time.RFC3339),
				"mountAdmission": admission,
				"reason":         reason,
				"message":        "test status",
				"desired":        map[string]interface{}{"policyFingerprint": policy.fingerprint},
				"observed":       facts,
			},
		}}
	}
	type admissionCase struct {
		name     string
		enabled  bool
		object   *unstructured.Unstructured
		wantCode codes.Code
		reason   string
		change   func(*Driver, *unstructured.Unstructured)
	}
	tests := []admissionCase{
		{
			name:     "disabled without status",
			enabled:  false,
			wantCode: codes.OK,
		},
		{
			name:     "allowed current status",
			enabled:  true,
			object:   newStatus("Allowed", "ReadyForNewMounts", time.Now()),
			wantCode: codes.OK,
		},
		{
			name:     "denied current status",
			enabled:  true,
			object:   newStatus("Denied", "UnsafeResidentMismatch", time.Now()),
			wantCode: codes.FailedPrecondition,
		},
		{
			name:     "allowed but stale status",
			enabled:  true,
			object:   newStatus("Allowed", "ReadyForNewMounts", time.Now().Add(-3*time.Minute)),
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "new policy with old allowed decision", enabled: true,
			object:   newStatus("Allowed", "AdmissionNotEnforced", time.Now()),
			wantCode: codes.FailedPrecondition,
			change: func(d *Driver, _ *unstructured.Unstructured) {
				d.mountAdmissionReadFile = func(string) ([]byte, error) {
					return []byte(strings.Replace(testEnforcedCompatibilityPolicy, "clientSetRevision: 1", "clientSetRevision: 2", 1)), nil
				}
			},
		},
		{
			name: "fresh controller with expired facts", enabled: true,
			object:   newStatus("Allowed", "ReadyForNewMounts", time.Now()),
			wantCode: codes.FailedPrecondition,
			change: func(_ *Driver, object *unstructured.Unstructured) {
				if err := unstructured.SetNestedField(object.Object, time.Now().Add(-2*time.Minute).UTC().Format(time.RFC3339Nano),
					"status", "observed", "factRenewTime"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "module changed since observation", enabled: true,
			object:   newStatus("Allowed", "ReadyForNewMounts", time.Now()),
			wantCode: codes.FailedPrecondition,
			change: func(d *Driver, _ *unstructured.Unstructured) {
				d.nodeFactReadFile = func(string) ([]byte, error) { return []byte("different"), nil }
			},
		},
		{
			name: "old pod facts after replacement", enabled: true,
			object:   newStatus("Allowed", "ReadyForNewMounts", time.Now()),
			wantCode: codes.FailedPrecondition,
			change: func(_ *Driver, object *unstructured.Unstructured) {
				if err := unstructured.SetNestedField(object.Object, "old-pod", "status", "observed", nodeFactAnnotationPrefix+"pod-uid"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "administrative deny before next reconciliation", enabled: true,
			object:   newStatus("Allowed", "ReadyForNewMounts", time.Now()),
			wantCode: codes.FailedPrecondition,
			change: func(d *Driver, _ *unstructured.Unstructured) {
				current := node.DeepCopy()
				current.Annotations = map[string]string{mountAdmissionAnnotation: "denied"}
				d.kubeClient = fake.NewSimpleClientset(current, pod.DeepCopy())
			},
		},
		{
			name: "host evidence cannot be read", enabled: true,
			object:   newStatus("Allowed", "ReadyForNewMounts", time.Now()),
			wantCode: codes.FailedPrecondition,
			change: func(d *Driver, _ *unstructured.Unstructured) {
				d.nodeFactReadFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
			},
		},
		{
			name: "driver container identity missing from both sources", enabled: true,
			object:   newStatus("Allowed", "ReadyForNewMounts", time.Now()),
			wantCode: codes.FailedPrecondition,
			change: func(d *Driver, object *unstructured.Unstructured) {
				current := pod.DeepCopy()
				current.Status.ContainerStatuses[0].ContainerID = ""
				d.kubeClient = fake.NewSimpleClientset(node.DeepCopy(), current)
				unstructured.RemoveNestedField(object.Object, "status", "observed", nodeFactAnnotationPrefix+"driver-container-id")
			},
		},
	}

	for _, test := range []struct {
		name, reason string
		change       func(*Driver, *unstructured.Unstructured)
	}{
		{"missing policy reader", "PolicyUnavailable", func(d *Driver, _ *unstructured.Unstructured) {
			d.mountAdmissionReadFile = nil
		}},
		{"missing policy path", "PolicyUnavailable", func(d *Driver, _ *unstructured.Unstructured) {
			d.mountAdmissionPolicyPath = ""
		}},
		{"unreadable policy", "PolicyUnavailable", func(d *Driver, _ *unstructured.Unstructured) {
			d.mountAdmissionReadFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
		}},
		{"malformed policy", "PolicyUnavailable", func(d *Driver, _ *unstructured.Unstructured) {
			d.mountAdmissionReadFile = func(string) ([]byte, error) { return []byte("invalid: ["), nil }
		}},
		{"missing node", "IdentityUnavailable", func(d *Driver, _ *unstructured.Unstructured) {
			d.kubeClient = fake.NewSimpleClientset(pod.DeepCopy())
		}},
		{"missing pod", "IdentityUnavailable", func(d *Driver, _ *unstructured.Unstructured) {
			d.kubeClient = fake.NewSimpleClientset(node.DeepCopy())
		}},
		{"deleting node", "IdentityChanged", func(d *Driver, _ *unstructured.Unstructured) {
			current := node.DeepCopy()
			current.DeletionTimestamp = &metav1.Time{Time: time.Now()}
			d.kubeClient = fake.NewSimpleClientset(current, pod.DeepCopy())
		}},
		{"deleting pod", "IdentityChanged", func(d *Driver, _ *unstructured.Unstructured) {
			current := pod.DeepCopy()
			current.DeletionTimestamp = &metav1.Time{Time: time.Now()}
			d.kubeClient = fake.NewSimpleClientset(node.DeepCopy(), current)
		}},
		{"pod assigned elsewhere", "IdentityChanged", func(d *Driver, _ *unstructured.Unstructured) {
			current := pod.DeepCopy()
			current.Spec.NodeName = "other-node"
			d.kubeClient = fake.NewSimpleClientset(node.DeepCopy(), current)
		}},
		{"missing observation time", "StatusStale", func(_ *Driver, object *unstructured.Unstructured) {
			unstructured.RemoveNestedField(object.Object, "status", "observedAt")
		}},
		{"missing runtime evidence", "EvidenceUnavailable", func(_ *Driver, object *unstructured.Unstructured) {
			unstructured.RemoveNestedField(object.Object, "status", "observed")
		}},
		{"missing heartbeat", "ReporterStale", func(_ *Driver, object *unstructured.Unstructured) {
			unstructured.RemoveNestedField(object.Object, "status", "observed", "factRenewTime")
		}},
		{"denied without diagnostic fields", "AdmissionDenied", func(_ *Driver, object *unstructured.Unstructured) {
			for _, field := range []string{"mountAdmission", "reason", "message"} {
				unstructured.RemoveNestedField(object.Object, "status", field)
			}
		}},
	} {
		tests = append(tests, admissionCase{
			name: test.name, enabled: true, object: newStatus("Allowed", "ReadyForNewMounts", time.Now()),
			wantCode: codes.FailedPrecondition, reason: test.reason, change: test.change,
		})
	}
	tests = append(tests, admissionCase{
		name: "status resource absent", enabled: true, wantCode: codes.FailedPrecondition, reason: "StatusUnavailable",
	})
	for _, field := range []string{"mountAdmission", "reason", "message"} {
		tests = append(tests, admissionCase{
			name: "malformed " + field, enabled: true, object: newStatus("Allowed", "ReadyForNewMounts", time.Now()),
			wantCode: codes.FailedPrecondition, reason: "StatusInvalid",
			change: func(_ *Driver, object *unstructured.Unstructured) {
				if err := unstructured.SetNestedField(object.Object, int64(1), "status", field); err != nil {
					t.Fatal(err)
				}
			},
		})
	}

	for _, field := range []string{
		"node-uid", "pod-uid", "pod-revision", "provider-id", "flavor",
		"driver-container-id", "loader-container-id", "driver-image-id", "loader-image-id",
		"driver-restart-count", "loader-restart-count", "boot-id", "kernel-version", "loaded-client",
	} {
		object := newStatus("Allowed", "ReadyForNewMounts", time.Now())
		if err := unstructured.SetNestedField(object.Object, "changed", "status", "observed", nodeFactAnnotationPrefix+field); err != nil {
			t.Fatal(err)
		}
		tests = append(tests, admissionCase{
			name: "changed " + field, enabled: true, object: object, wantCode: codes.FailedPrecondition,
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := []runtime.Object{}
			if test.object != nil {
				objects = append(objects, test.object)
			}
			driver := &Driver{
				CSIDriver:                   CSIDriver{NodeID: nodeName},
				podNamespace:                namespace,
				podName:                     pod.Name,
				kubeClient:                  fake.NewSimpleClientset(node.DeepCopy(), pod.DeepCopy()),
				mountAdmissionPolicyEnabled: test.enabled,
				mountAdmissionPolicyPath:    "test-policy",
				mountAdmissionReadFile: func(string) ([]byte, error) {
					return []byte(testEnforcedCompatibilityPolicy), nil
				},
				nodeFactReadFile: func(path string) ([]byte, error) {
					switch path {
					case "/proc/sys/kernel/random/boot_id":
						return []byte("boot-0"), nil
					case "/proc/sys/kernel/osrelease":
						return []byte("test-kernel"), nil
					case "/sys/module/lustre/version":
						return []byte("2.17.0_current"), nil
					default:
						return nil, os.ErrNotExist
					}
				},
			}
			if test.change != nil {
				test.change(driver, test.object)
			}
			driver.dynamicClient = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
				runtime.NewScheme(),
				map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"},
				objects...,
			)
			err := driver.checkNodeMountAdmission(context.Background())
			if got := status.Code(err); got != test.wantCode {
				t.Fatalf("checkNodeMountAdmission() code = %s, want %s, error = %v", got, test.wantCode, err)
			}
			if test.reason != "" && (err == nil || !strings.Contains(err.Error(), test.reason)) {
				t.Fatalf("checkNodeMountAdmission() error = %v, want reason %s", err, test.reason)
			}
		})
	}
}

func TestCheckNodeMountAdmissionHonorsDynamicPolicyDisable(t *testing.T) {
	driver := &Driver{
		CSIDriver:                   CSIDriver{NodeID: "node-0"},
		mountAdmissionPolicyEnabled: true,
		mountAdmissionPolicyPath:    "test-policy",
		mountAdmissionReadFile: func(string) ([]byte, error) {
			return []byte(testCompatibilityPolicy), nil
		},
	}
	if err := driver.checkNodeMountAdmission(context.Background()); err != nil {
		t.Fatalf("disabled policy denied mount: %v", err)
	}
}

func TestMountAdmissionConditionPreservesTransitionTime(t *testing.T) {
	previous := "2026-09-17T12:00:00Z"
	object := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{map[string]interface{}{
				"type":               "ReadyForNewMounts",
				"status":             "False",
				"reason":             "ReporterStale",
				"message":            "old",
				"lastTransitionTime": previous,
			}},
		},
	}}
	condition := mountAdmissionCondition(object, computedNodeStatus{
		mountAdmission: "Denied",
		reason:         "ReporterStale",
		message:        "new",
	}, metav1.Now().Time)
	if condition["lastTransitionTime"] != previous {
		t.Fatalf("lastTransitionTime = %q, want %q", condition["lastTransitionTime"], previous)
	}
}
