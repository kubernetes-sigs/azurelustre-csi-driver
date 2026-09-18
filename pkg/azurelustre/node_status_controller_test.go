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
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

const (
	testCompatibilityPolicy = `apiVersion: azurelustre.csi.azure.com/v1alpha1
kind: AzureLustreCompatibilityPolicy
clientSetRevision: 7
cacheTTLSeconds: 3600
mountAdmission:
  enforce: false
images:
  driver:
    jammy:
      desiredDigest: ""
      approvedDigests: []
  loader:
    jammy:
      desiredDigest: ""
      approvedDigests: []
clients:
  jammy:
    desired:
      version: "2.17.0"
      shaSuffix: "current"
    compatible:
      - version: "2.17.0"
        shaSuffix: "current"
      - version: "2.18.0"
        shaSuffix: "newer"
bootstrap:
  enabled: false
  expiresAt: ""
  csiImage:
    repository: example.invalid/azurelustre
    tag: test
    digest: ""
`
)

func TestStatusControllerRunTestMode(t *testing.T) {
	driver := &Driver{
		CSIDriver: CSIDriver{Name: DefaultDriverName, Version: driverVersion},
		podRole:   statusControllerPod,
	}
	if err := driver.Run("", true); err != nil {
		t.Fatalf("status-controller Run() error = %v", err)
	}
}

func TestLoadCompatibilityPolicyCachesTemporaryReadFailure(t *testing.T) {
	namespace := "kube-system"
	name := "compatibility-policy"
	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string]string{compatibilityPolicyKey: testCompatibilityPolicy},
	})
	driver := &Driver{
		kubeClient:                   client,
		podNamespace:                 namespace,
		compatibilityPolicyConfigMap: name,
	}

	policy, err := driver.loadCompatibilityPolicy(context.Background())
	if err != nil {
		t.Fatalf("loadCompatibilityPolicy() error = %v", err)
	}
	if policy.ClientSetRevision != 7 {
		t.Fatalf("client set revision = %d, want 7", policy.ClientSetRevision)
	}
	client.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("temporary transport failure")
	})
	if _, err := driver.loadCompatibilityPolicy(context.Background()); err != nil {
		t.Fatalf("cached loadCompatibilityPolicy() error = %v", err)
	}

	driver.compatibilityPolicyMu.Lock()
	driver.cachedCompatibilityPolicyAt = time.Now().Add(-2 * time.Hour)
	driver.compatibilityPolicyMu.Unlock()
	if _, err := driver.loadCompatibilityPolicy(context.Background()); err == nil {
		t.Fatal("expired compatibility policy cache unexpectedly succeeded")
	}
}

func TestLoadCompatibilityPolicyDoesNotCacheDeletion(t *testing.T) {
	namespace := "kube-system"
	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
		Data:       map[string]string{compatibilityPolicyKey: testCompatibilityPolicy},
	})
	driver := &Driver{kubeClient: client, podNamespace: namespace, compatibilityPolicyConfigMap: "policy"}
	if _, err := driver.loadCompatibilityPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.CoreV1().ConfigMaps(namespace).Delete(context.Background(), "policy", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.loadCompatibilityPolicy(context.Background()); err == nil {
		t.Fatal("deleted policy was accepted from cache")
	}
	client.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("temporary transport failure after deletion")
	})
	if _, err := driver.loadCompatibilityPolicy(context.Background()); err == nil {
		t.Fatal("deleted policy was resurrected after a transport failure")
	}
}

func TestDesiredDaemonSetRevisionsRequiresMatchingObservedTemplate(t *testing.T) {
	_, _, _, daemonset, revision := testNodeStatusObjects("kube-system")
	revisions := []appsv1.ControllerRevision{*revision}
	if got := desiredDaemonSetRevisions([]appsv1.DaemonSet{*daemonset}, revisions)[daemonset.Name]; got != "desired-hash" {
		t.Fatalf("matching template revision = %q", got)
	}
	daemonset.Generation = 2
	daemonset.Status.ObservedGeneration = 1
	if got := desiredDaemonSetRevisions([]appsv1.DaemonSet{*daemonset}, revisions)[daemonset.Name]; got != "" {
		t.Fatalf("unobserved generation was classified as %q", got)
	}
	daemonset.Status.ObservedGeneration = 2
	daemonset.Spec.Template.Annotations = map[string]string{"staged": "new"}
	if got := desiredDaemonSetRevisions([]appsv1.DaemonSet{*daemonset}, revisions)[daemonset.Name]; got != "" {
		t.Fatalf("old history was treated as the new desired revision: %q", got)
	}
	daemonset.Spec.Template.Annotations = nil
	revisions = append(revisions, *revision.DeepCopy())
	revisions[1].Name += "-duplicate"
	if got := desiredDaemonSetRevisions([]appsv1.DaemonSet{*daemonset}, revisions)[daemonset.Name]; got != "" {
		t.Fatalf("ambiguous histories were accepted: %q", got)
	}
}

func TestLoadCompatibilityPolicyRejectsUnknownSchema(t *testing.T) {
	namespace := "kube-system"
	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
		Data: map[string]string{
			compatibilityPolicyKey: strings.Replace(
				testCompatibilityPolicy,
				"kind: AzureLustreCompatibilityPolicy",
				"kind: AzureLustreCompatibilityPolicy\nunexpected: true",
				1),
		},
	})
	driver := &Driver{
		kubeClient:                   client,
		podNamespace:                 namespace,
		compatibilityPolicyConfigMap: "policy",
	}
	if _, err := driver.loadCompatibilityPolicy(context.Background()); err == nil {
		t.Fatal("policy containing an unknown field unexpectedly succeeded")
	}
}

func TestParseCompatibilityPolicyRequiresApprovedDesiredDigestsWhenEnforced(t *testing.T) {
	raw := strings.Replace(testCompatibilityPolicy, "enforce: false", "enforce: true", 1)
	if _, err := parseCompatibilityPolicy([]byte(raw)); err == nil {
		t.Fatal("enforced policy with empty image digests unexpectedly succeeded")
	}
}

func TestParseCompatibilityPolicyRequiresBootstrapDigest(t *testing.T) {
	raw := strings.Replace(testCompatibilityPolicy, "enabled: false", "enabled: true", 1)
	raw = strings.Replace(raw, `expiresAt: ""`,
		`expiresAt: "`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"`, 1)
	if _, err := parseCompatibilityPolicy([]byte(raw)); err == nil {
		t.Fatal("enabled bootstrap policy without an immutable image digest unexpectedly succeeded")
	}

	raw = strings.Replace(raw, `digest: ""`, `digest: "sha256:`+strings.Repeat("a", 64)+`"`, 1)
	if _, err := parseCompatibilityPolicy([]byte(raw)); err != nil {
		t.Fatalf("enabled bootstrap policy with an immutable image digest failed: %v", err)
	}
}

func TestComputeNodeStatusClassifiesCompatibleSkew(t *testing.T) {
	namespace := "kube-system"
	node, pod, lease, daemonset, revision := testNodeStatusObjects(namespace)
	driver := &Driver{kubeClient: fake.NewSimpleClientset(daemonset, revision)}
	desiredRevisions := map[string]string{daemonset.Name: "desired-hash"}
	policy := &compatibilityPolicy{
		ClientSetRevision: 7,
		MountAdmission:    compatibilityPolicyAdmission{Enforce: true},
		Images: compatibilityPolicyImages{
			Driver: map[string]compatibilityPolicyImage{
				"jammy": {
					DesiredDigest:   "sha256:" + strings.Repeat("a", 64),
					ApprovedDigests: []string{"sha256:" + strings.Repeat("a", 64)},
				},
			},
			Loader: map[string]compatibilityPolicyImage{
				"jammy": {
					DesiredDigest:   "sha256:" + strings.Repeat("b", 64),
					ApprovedDigests: []string{"sha256:" + strings.Repeat("b", 64)},
				},
			},
		},
		Clients: map[string]compatibilityPolicyOS{
			"jammy": {
				Desired: clientIdentity{Version: "2.17.0", ShaSuffix: "current"},
				Compatible: []clientIdentity{
					{Version: "2.17.0", ShaSuffix: "current"},
					{Version: "2.18.0", ShaSuffix: "newer"},
				},
			},
		},
	}

	status := driver.computeNodeStatus(node, pod, lease, policy, nil, desiredRevisions)
	if status.clientCompatibility != "Exact" || status.csiDelivery != "Current" {
		t.Fatalf("exact status = client %q, CSI %q", status.clientCompatibility, status.csiDelivery)
	}
	if status.mountAdmission != "Allowed" || status.securityCompliance != "Approved" {
		t.Fatalf("status admission = %q with security %q, want approved", status.mountAdmission, status.securityCompliance)
	}

	node.Annotations = map[string]string{mountAdmissionAnnotation: "denied"}
	status = driver.computeNodeStatus(node, pod, lease, policy, nil, desiredRevisions)
	if status.mountAdmission != "Denied" || status.reason != "AdministrativeQuiescence" {
		t.Fatalf("quiesced status = admission %q, reason %q", status.mountAdmission, status.reason)
	}
	if status.clientCompatibility != "Exact" || status.reporterFreshness != "Current" || status.securityCompliance != "Approved" {
		t.Fatalf("quiescence hid the health evidence: %+v", status)
	}
	node.Annotations = nil

	lease.Annotations[nodeFactAnnotationPrefix+"loader-image-id"] = "docker-pullable://loader@sha256:" + strings.Repeat("c", 64)
	pod.Status.InitContainerStatuses[0].ImageID = "docker-pullable://loader@sha256:" + strings.Repeat("c", 64)
	status = driver.computeNodeStatus(node, pod, lease, policy, nil, desiredRevisions)
	if status.securityCompliance != "Withdrawn" || status.mountAdmission != "Denied" {
		t.Fatalf("withdrawn status = security %q, admission %q", status.securityCompliance, status.mountAdmission)
	}
	lease.Annotations[nodeFactAnnotationPrefix+"loader-image-id"] = "docker-pullable://loader@sha256:" + strings.Repeat("b", 64)
	pod.Status.InitContainerStatuses[0].ImageID = "docker-pullable://loader@sha256:" + strings.Repeat("b", 64)

	lease.Annotations[nodeFactAnnotationPrefix+"loaded-client"] = "2.18.0_newer"
	status = driver.computeNodeStatus(node, pod, lease, policy, nil, desiredRevisions)
	if status.clientCompatibility != "CompatibleNonCurrent" {
		t.Fatalf("reverse compatible skew = %q, want CompatibleNonCurrent", status.clientCompatibility)
	}

	lease.Spec.RenewTime = &metav1.MicroTime{Time: time.Now().Add(-2 * time.Minute)}
	status = driver.computeNodeStatus(node, pod, lease, policy, nil, desiredRevisions)
	if status.reporterFreshness != "Stale" || status.clientCompatibility != "ClientUnknown" {
		t.Fatalf("stale status = reporter %q, client %q", status.reporterFreshness, status.clientCompatibility)
	}
}

func TestComputeNodeStatusAllowsFiniteLegacyBootstrap(t *testing.T) {
	namespace := "kube-system"
	node, pod, _, daemonset, revision := testNodeStatusObjects(namespace)
	pod.Spec.Containers = []corev1.Container{{
		Name:  "azurelustre",
		Image: "example.invalid/azurelustre:legacy",
	}}
	driver := &Driver{kubeClient: fake.NewSimpleClientset(daemonset, revision)}
	desiredRevisions := map[string]string{daemonset.Name: "desired-hash"}
	policy := &compatibilityPolicy{
		MountAdmission: compatibilityPolicyAdmission{Enforce: true},
		Bootstrap: compatibilityPolicyBootstrap{
			Enabled:   true,
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			CSIImage: compatibilityPolicyCSIImage{
				Repository: "example.invalid/azurelustre",
				Tag:        "legacy",
				Digest:     "sha256:" + strings.Repeat("a", 64),
			},
		},
	}

	status := driver.computeNodeStatus(node, pod, nil, policy, nil, desiredRevisions)
	if status.clientCompatibility != "LegacyBootstrapCompatible" || status.mountAdmission != "Allowed" {
		t.Fatalf("bootstrap status = client %q, admission %q", status.clientCompatibility, status.mountAdmission)
	}

	policy.Bootstrap.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	status = driver.computeNodeStatus(node, pod, nil, policy, nil, desiredRevisions)
	if status.clientCompatibility != "ClientUnknown" || status.mountAdmission != "Denied" {
		t.Fatalf("expired bootstrap status = client %q, admission %q", status.clientCompatibility, status.mountAdmission)
	}
}

func TestReconcileNodeStatusesWritesStatusResource(t *testing.T) {
	namespace := "kube-system"
	node, pod, lease, daemonset, revision := testNodeStatusObjects(namespace)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
		Data:       map[string]string{compatibilityPolicyKey: testCompatibilityPolicy},
	}
	driver := &Driver{
		kubeClient: fake.NewSimpleClientset(node, pod, lease, daemonset, revision, configMap),
		dynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			runtime.NewScheme(),
			map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"},
		),
		podNamespace:                 namespace,
		compatibilityPolicyConfigMap: "policy",
	}

	if err := driver.reconcileNodeStatuses(context.Background()); err != nil {
		t.Fatalf("reconcileNodeStatuses() error = %v", err)
	}
	object, err := driver.dynamicClient.Resource(nodeStatusGVR).Namespace(namespace).Get(
		context.Background(), node.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get reconciled status: %v", err)
	}
	clientState, _ := unstructuredString(object.Object, "status", "clientCompatibility")
	if clientState != "Exact" {
		t.Fatalf("clientCompatibility = %q, want Exact", clientState)
	}
	reporterState, _ := unstructuredString(object.Object, "status", "reporterFreshness")
	if reporterState != "Current" {
		t.Fatalf("reporterFreshness = %q, want Current", reporterState)
	}
}

func TestReconcileNodeStatusesDetectsMissingEligiblePod(t *testing.T) {
	namespace := "kube-system"
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "node-0",
		UID:  types.UID("node-uid"),
		Labels: map[string]string{
			"kubernetes.io/os":                      "linux",
			"kubernetes.azure.com/os-sku-effective": "Ubuntu2204",
		},
	}}
	daemonset := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-ds",
			Namespace: namespace,
			Labels: map[string]string{
				"app":    "csi-azurelustre-node",
				"flavor": "jammy",
			},
		},
		Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				NodeSelector: map[string]string{"kubernetes.io/os": "linux"},
				Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{{
							MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key:      "kubernetes.azure.com/os-sku-effective",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"Ubuntu2204"},
							}},
						}},
					},
				}},
			},
		}},
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
		Data:       map[string]string{compatibilityPolicyKey: testCompatibilityPolicy},
	}
	driver := &Driver{
		kubeClient: fake.NewSimpleClientset(node, daemonset, configMap),
		dynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			runtime.NewScheme(),
			map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"},
		),
		podNamespace:                 namespace,
		compatibilityPolicyConfigMap: "policy",
	}

	if err := driver.reconcileNodeStatuses(context.Background()); err != nil {
		t.Fatalf("reconcileNodeStatuses() error = %v", err)
	}
	object, err := driver.dynamicClient.Resource(nodeStatusGVR).Namespace(namespace).Get(
		context.Background(), node.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get reconciled status: %v", err)
	}
	coverage, _ := unstructuredString(object.Object, "status", "schedulingCoverage")
	if coverage != "NoEligibleNodePod" {
		t.Fatalf("schedulingCoverage = %q, want NoEligibleNodePod", coverage)
	}
}

func TestReconcileNodeStatusesContinuesAfterStatusWriteFailure(t *testing.T) {
	namespace := "kube-system"
	node0, pod0, lease0, daemonset, revision := testNodeStatusObjects(namespace)
	node1, pod1, lease1, _, _ := testNodeStatusObjects(namespace)
	node1.Name = "node-1"
	node1.UID = types.UID("node-uid-1")
	pod1.Name = "node-pod-1"
	pod1.UID = types.UID("pod-uid-1")
	pod1.Spec.NodeName = node1.Name
	lease1.Name = "node-fact-1"
	lease1.Annotations[nodeFactAnnotationPrefix+"node-name"] = node1.Name
	lease1.Annotations[nodeFactAnnotationPrefix+"node-uid"] = string(node1.UID)
	lease1.Annotations[nodeFactAnnotationPrefix+"pod-name"] = pod1.Name
	lease1.Annotations[nodeFactAnnotationPrefix+"pod-uid"] = string(pod1.UID)
	holder := string(pod1.UID)
	lease1.Spec.HolderIdentity = &holder

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
		Data:       map[string]string{compatibilityPolicyKey: testCompatibilityPolicy},
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"},
	)
	dynamicClient.PrependReactor("create", "azurelustrenodestatuses",
		func(action clienttesting.Action) (bool, runtime.Object, error) {
			createAction, ok := action.(clienttesting.CreateAction)
			if ok {
				object, objectOK := createAction.GetObject().(*unstructured.Unstructured)
				if objectOK && object.GetName() == node0.Name {
					return true, nil, errors.New("injected status write failure")
				}
			}
			return false, nil, nil
		})
	driver := &Driver{
		kubeClient: fake.NewSimpleClientset(
			node0, pod0, lease0, node1, pod1, lease1, daemonset, revision, configMap),
		dynamicClient:                dynamicClient,
		podNamespace:                 namespace,
		compatibilityPolicyConfigMap: "policy",
	}

	if err := driver.reconcileNodeStatuses(context.Background()); err == nil {
		t.Fatal("reconcileNodeStatuses() unexpectedly ignored the injected write failure")
	}
	if _, err := dynamicClient.Resource(nodeStatusGVR).Namespace(namespace).Get(
		context.Background(), node1.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("healthy node status was not written after another node failed: %v", err)
	}
}

func testNodeStatusObjects(namespace string) (
	*corev1.Node,
	*corev1.Pod,
	*coordinationv1.Lease,
	*appsv1.DaemonSet,
	*appsv1.ControllerRevision,
) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-0", UID: types.UID("node-uid")},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{
			BootID: "boot-0", KernelVersion: "test-kernel",
		}},
	}
	daemonset := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-ds",
			Namespace: namespace,
			UID:       types.UID("ds-uid"),
			Labels:    map[string]string{"app": "csi-azurelustre-node", "flavor": "jammy"},
		},
	}
	controller := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-pod",
			Namespace: namespace,
			UID:       types.UID("pod-uid"),
			Labels: map[string]string{
				"app":                      "csi-azurelustre-node",
				"flavor":                   "jammy",
				"controller-revision-hash": "desired-hash",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "DaemonSet",
				Name:       daemonset.Name,
				UID:        daemonset.UID,
				Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{NodeName: node.Name},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:        "azurelustre",
				ImageID:     "docker-pullable://driver@sha256:" + strings.Repeat("a", 64),
				ContainerID: "containerd://driver-0",
			}},
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:        "lustre-loader",
				ImageID:     "docker-pullable://loader@sha256:" + strings.Repeat("b", 64),
				ContainerID: "containerd://loader-0",
			}},
		},
	}
	duration := int32(90)
	holder := string(pod.UID)
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-fact",
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/component": "node-fact"},
			Annotations: map[string]string{
				nodeFactAnnotationPrefix + "schema-version":       nodeFactSchemaVersion,
				nodeFactAnnotationPrefix + "node-name":            node.Name,
				nodeFactAnnotationPrefix + "node-uid":             string(node.UID),
				nodeFactAnnotationPrefix + "boot-id":              node.Status.NodeInfo.BootID,
				nodeFactAnnotationPrefix + "kernel-version":       node.Status.NodeInfo.KernelVersion,
				nodeFactAnnotationPrefix + "pod-name":             pod.Name,
				nodeFactAnnotationPrefix + "pod-uid":              string(pod.UID),
				nodeFactAnnotationPrefix + "pod-revision":         "desired-hash",
				nodeFactAnnotationPrefix + "flavor":               "jammy",
				nodeFactAnnotationPrefix + "loaded-client":        "2.17.0_current",
				nodeFactAnnotationPrefix + "driver-image-id":      "docker-pullable://driver@sha256:" + strings.Repeat("a", 64),
				nodeFactAnnotationPrefix + "loader-image-id":      "docker-pullable://loader@sha256:" + strings.Repeat("b", 64),
				nodeFactAnnotationPrefix + "driver-container-id":  "containerd://driver-0",
				nodeFactAnnotationPrefix + "loader-container-id":  "containerd://loader-0",
				nodeFactAnnotationPrefix + "driver-restart-count": "0",
				nodeFactAnnotationPrefix + "loader-restart-count": "0",
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &duration,
			RenewTime:            &metav1.MicroTime{Time: time.Now()},
		},
	}
	revision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      daemonset.Name + "-desired-hash",
			Namespace: namespace,
			Labels:    map[string]string{appsv1.DefaultDaemonSetUniqueLabelKey: "desired-hash"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "DaemonSet",
				Name:       daemonset.Name,
				UID:        daemonset.UID,
				Controller: &controller,
			}},
		},
		Revision: 2,
		Data:     runtime.RawExtension{Raw: []byte(`{"spec":{"template":{"$patch":"replace","metadata":{},"spec":{}}}}`)},
	}
	return node, pod, lease, daemonset, revision
}

func unstructuredString(object map[string]interface{}, fields ...string) (string, bool) {
	current := interface{}(object)
	for _, field := range fields {
		values, ok := current.(map[string]interface{})
		if !ok {
			return "", false
		}
		current, ok = values[field]
		if !ok {
			return "", false
		}
	}
	value, ok := current.(string)
	return value, ok
}

func TestReconcileNodeStatusesRejectsIncompleteSnapshots(t *testing.T) {
	for _, resource := range []string{"nodes", "pods", "leases", "daemonsets", "controllerrevisions", "azurelustrenodestatuses"} {
		t.Run(resource, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			statusClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"})
			failure := errors.New("injected list failure")
			reactor := func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, failure }
			client.PrependReactor("list", resource, reactor)
			statusClient.PrependReactor("list", resource, reactor)
			driver := &Driver{kubeClient: client, dynamicClient: statusClient, podNamespace: "driver"}

			require.ErrorIs(t, driver.reconcileNodeStatuses(t.Context()), failure)
			for _, action := range statusClient.Actions() {
				assert.Equal(t, "list", action.GetVerb(), "incomplete snapshots must not write or garbage collect statuses")
			}
		})
	}
}

func TestReconcileNodeStatusesHandlesMissingAndCompetingReporters(t *testing.T) {
	for _, test := range []struct {
		name, reason string
	}{
		{"missing pod with retained fact", "NodePodMissing"},
		{"competing pods", "AmbiguousNodePods"},
		{"unscheduled pod", "NodePodMissing"},
		{"orphaned reporters", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			node, pod, lease, daemonset, revision := testNodeStatusObjects("driver")
			objects := []runtime.Object{node, daemonset, revision, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "driver"},
				Data:       map[string]string{compatibilityPolicyKey: testEnforcedCompatibilityPolicy},
			}}
			switch test.name {
			case "missing pod with retained fact":
				objects = append(objects, lease)
			case "competing pods":
				second := pod.DeepCopy()
				second.Name, second.UID = "other-pod", "other-uid"
				objects = append(objects, pod, second, lease)
			case "unscheduled pod":
				pod.Spec.NodeName = ""
				objects = append(objects, pod)
			case "orphaned reporters":
				objects = []runtime.Object{pod, lease}
			}
			client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"})
			driver := &Driver{
				kubeClient: fake.NewSimpleClientset(objects...), dynamicClient: client,
				podNamespace: "driver", compatibilityPolicyConfigMap: "policy",
			}
			require.NoError(t, driver.reconcileNodeStatuses(t.Context()))
			statuses, err := client.Resource(nodeStatusGVR).Namespace("driver").List(t.Context(), metav1.ListOptions{})
			require.NoError(t, err)
			if test.reason == "" {
				assert.Empty(t, statuses.Items, "a deleted Node must not acquire a new status")
				return
			}
			require.Len(t, statuses.Items, 1)
			assert.Equal(t, node.Name, statuses.Items[0].GetName())
			reason, _, err := unstructured.NestedString(statuses.Items[0].Object, "status", "reason")
			require.NoError(t, err)
			assert.Equal(t, test.reason, reason)
			admission, _, err := unstructured.NestedString(statuses.Items[0].Object, "status", "mountAdmission")
			require.NoError(t, err)
			assert.Equal(t, "Denied", admission)
		})
	}
}

func TestParseCompatibilityPolicyRejectsInvalidSafetyFields(t *testing.T) {
	for _, test := range []struct {
		name, message string
		change        func(*compatibilityPolicy)
	}{
		{"no clients", "contains no clients", func(p *compatibilityPolicy) { p.Clients = nil }},
		{"wrong schema", "unsupported compatibility policy", func(p *compatibilityPolicy) { p.APIVersion = "unknown/v1" }},
		{"zero revision", "revision and cache TTL must be positive", func(p *compatibilityPolicy) { p.ClientSetRevision = 0 }},
		{"negative TTL", "revision and cache TTL must be positive", func(p *compatibilityPolicy) { p.CacheTTLSeconds = -1 }},
		{"missing desired identity", "empty desired client", func(p *compatibilityPolicy) {
			p.Clients["jammy"] = compatibilityPolicyOS{Desired: clientIdentity{Version: "2.17.0"}}
		}},
		{"missing compatible identity", "empty compatible client", func(p *compatibilityPolicy) {
			client := p.Clients["jammy"]
			client.Compatible = []clientIdentity{{ShaSuffix: "current"}}
			p.Clients["jammy"] = client
		}},
		{"invalid approved digest", "approved image digest is invalid", func(p *compatibilityPolicy) {
			p.Images.Driver["jammy"] = compatibilityPolicyImage{ApprovedDigests: []string{"sha256:invalid"}}
		}},
		{"invalid bootstrap expiration", "bootstrap expiration is invalid", func(p *compatibilityPolicy) {
			p.Bootstrap.Enabled, p.Bootstrap.ExpiresAt = true, "not-a-time"
		}},
		{"missing bootstrap repository", "bootstrap image must be fully specified", func(p *compatibilityPolicy) {
			p.Bootstrap.Enabled, p.Bootstrap.ExpiresAt = true, "2099-01-01T00:00:00Z"
			p.Bootstrap.CSIImage.Repository = ""
		}},
		{"zero bootstrap expiration", "bootstrap expiration must be finite", func(p *compatibilityPolicy) {
			p.Bootstrap.Enabled, p.Bootstrap.ExpiresAt = true, time.Time{}.Format(time.RFC3339)
			p.Bootstrap.CSIImage.Digest = "sha256:" + strings.Repeat("a", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := parseCompatibilityPolicy([]byte(testCompatibilityPolicy))
			require.NoError(t, err)
			test.change(policy)
			raw, err := json.Marshal(policy)
			require.NoError(t, err)
			_, err = parseCompatibilityPolicy(raw)
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestComputeNodeStatusDeniesIncompleteCompatibilityEvidence(t *testing.T) {
	for _, test := range []struct {
		name, reason, clientState string
		change                    func(*compatibilityPolicy, *coordinationv1.Lease, map[string]string)
	}{
		{"missing flavor policy", "FlavorPolicyMissing", "ClientUnknown", func(p *compatibilityPolicy, _ *coordinationv1.Lease, _ map[string]string) {
			delete(p.Clients, "jammy")
		}},
		{"unloaded client", "LoadedClientUnknown", "ClientUnavailable", func(_ *compatibilityPolicy, lease *coordinationv1.Lease, _ map[string]string) {
			delete(lease.Annotations, nodeFactAnnotationPrefix+"loaded-client")
		}},
		{"incompatible client", "UnsupportedLoadedClient", "UnsafeResidentMismatch", func(_ *compatibilityPolicy, lease *coordinationv1.Lease, _ map[string]string) {
			lease.Annotations[nodeFactAnnotationPrefix+"loaded-client"] = "2.16.0_unsupported"
		}},
		{"unknown desired revision", "CSIDeliveryUnknown", "Exact", func(_ *compatibilityPolicy, _ *coordinationv1.Lease, revisions map[string]string) {
			clear(revisions)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			node, pod, lease, daemonset, _ := testNodeStatusObjects("driver")
			policy, err := parseCompatibilityPolicy([]byte(testEnforcedCompatibilityPolicy))
			require.NoError(t, err)
			revisions := map[string]string{daemonset.Name: "desired-hash"}
			test.change(policy, lease, revisions)
			driver := &Driver{}
			status := driver.computeNodeStatus(node, pod, lease, policy, nil, revisions)
			assert.Equal(t, "Denied", status.mountAdmission)
			assert.Equal(t, "Current", status.reporterFreshness)
			assert.Equal(t, test.reason, status.reason)
			assert.Equal(t, test.clientState, status.clientCompatibility)
		})
	}
}
