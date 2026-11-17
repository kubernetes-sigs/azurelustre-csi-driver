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
	"encoding/json"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	"sigs.k8s.io/yaml"
)

func TestParseStaticFlavorImagePolicy(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/azurelustre-compatibility-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var configMap corev1.ConfigMap
	if err := yaml.UnmarshalStrict(raw, &configMap); err != nil {
		t.Fatal(err)
	}
	policy, err := parseCompatibilityPolicy([]byte(configMap.Data[compatibilityPolicyKey]))
	if err != nil {
		t.Fatal(err)
	}
	for _, images := range []map[string]compatibilityPolicyImage{policy.Images.Driver, policy.Images.Loader} {
		for _, flavor := range []string{"jammy", "noble", "azurelinux3"} {
			image, ok := images[flavor]
			if !ok || imagePolicyConfigured(image) {
				t.Fatalf("default %q image policy must be present and unconfigured", flavor)
			}
		}
	}
}

func TestReconcileNodeStatusesMixedFlavorImages(t *testing.T) {
	policy, raw := testMixedFlavorImagePolicy(t)
	namespace := "driver"
	fixtures := []*flavorImageFixture{
		newFlavorImageFixture(t, namespace, "jammy", policy),
		newFlavorImageFixture(t, namespace, "noble", policy),
	}
	objects := []runtime.Object{&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: namespace},
		Data:       map[string]string{compatibilityPolicyKey: string(raw)},
	}}
	for _, fixture := range fixtures {
		objects = append(objects, fixture.node, fixture.pod, fixture.lease, fixture.daemonset, fixture.revision)
	}
	client := fake.NewSimpleClientset(objects...)
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"})
	controller := &Driver{kubeClient: client, dynamicClient: dynamicClient, podNamespace: namespace, compatibilityPolicyConfigMap: "policy"}
	for _, prior := range []bool{false, true} {
		if prior {
			for _, fixture := range fixtures {
				flavor := fixture.pod.Labels["flavor"]
				digest := policy.Images.Driver[flavor].ApprovedDigests[1]
				fixture.setImageDigests(digest, digest)
				fixture.pod.Spec.Containers[0].Image = "example.invalid/azurelustre:prior-" + flavor
				fixture.pod.Spec.InitContainers[0].Image = "example.invalid/azurelustre:prior-" + flavor
				fixture.pod.Labels["controller-revision-hash"] = "prior-hash"
				fixture.lease.Annotations[nodeFactAnnotationPrefix+"pod-revision"] = "prior-hash"
				if _, err := client.CoreV1().Pods(namespace).Update(t.Context(), fixture.pod, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				if _, err := client.CoordinationV1().Leases(namespace).Update(t.Context(), fixture.lease, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := controller.reconcileNodeStatuses(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, fixture := range fixtures {
			object, err := dynamicClient.Resource(nodeStatusGVR).Namespace(namespace).Get(t.Context(), fixture.node.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			security, delivery := "Approved", "Current"
			if prior {
				security, delivery = "NonCurrentApproved", "NonCurrent"
			}
			for field, want := range map[string]string{
				"securityCompliance": security, "csiDelivery": delivery, "mountAdmission": "Allowed", "clientCompatibility": "Exact",
			} {
				if got, _, err := unstructured.NestedString(object.Object, "status", field); err != nil || got != want {
					t.Fatalf("%s prior=%t %s=%q, want %q: %v", fixture.node.Name, prior, field, got, want, err)
				}
			}
			flavor := fixture.pod.Labels["flavor"]
			for field, want := range map[string]string{
				"flavor": flavor, "driverImageDigest": policy.Images.Driver[flavor].DesiredDigest,
				"loaderImageDigest": policy.Images.Loader[flavor].DesiredDigest,
			} {
				if got, _, err := unstructured.NestedString(object.Object, "status", "desired", field); err != nil || got != want {
					t.Fatalf("%s desired %s=%q, want %q: %v", flavor, field, got, want, err)
				}
			}
			nodeDriver := fixture.admissionDriver(raw)
			nodeDriver.kubeClient, nodeDriver.dynamicClient = client, dynamicClient
			if err := nodeDriver.checkNodeMountAdmission(t.Context()); err != nil {
				t.Fatalf("%s prior=%t admission failed: %v", fixture.node.Name, prior, err)
			}
		}
	}
}

func TestFlavorImagePolicyRejectsCrossFlavorAuthorization(t *testing.T) {
	for _, flavor := range []string{"jammy", "noble"} {
		for _, scenario := range []string{"other driver", "other loader", "other prior driver", "spoofed flavor", "missing flavor", "unconfigured flavor"} {
			t.Run(flavor+"/"+scenario, func(t *testing.T) {
				policy, raw := testMixedFlavorImagePolicy(t)
				other := "noble"
				if flavor == other {
					other = "jammy"
				}
				fixture := newFlavorImageFixture(t, "driver", flavor, policy)
				ownDigest := policy.Images.Driver[flavor].DesiredDigest
				otherDigest := policy.Images.Driver[other].DesiredDigest
				switch scenario {
				case "other driver":
					fixture.setImageDigests(otherDigest, ownDigest)
				case "other loader":
					fixture.setImageDigests(ownDigest, otherDigest)
				case "other prior driver":
					fixture.setImageDigests(policy.Images.Driver[other].ApprovedDigests[1], ownDigest)
				case "spoofed flavor":
					fixture.setImageDigests(otherDigest, otherDigest)
					fixture.lease.Annotations[nodeFactAnnotationPrefix+"flavor"] = other
				case "missing flavor":
					delete(fixture.pod.Labels, "flavor")
				case "unconfigured flavor":
					delete(policy.Images.Driver, flavor)
					delete(policy.Images.Loader, flavor)
					policy, raw = marshalTestImagePolicy(t, policy)
				}
				driver := fixture.admissionDriver(raw)
				computed := driver.computeNodeStatus(fixture.node, fixture.pod, fixture.lease, policy, nil,
					map[string]string{fixture.daemonset.Name: "desired-hash"})
				controllerReason, nodeReason := "ImageDigestWithdrawn", "ImageDigestWithdrawn"
				switch scenario {
				case "spoofed flavor", "missing flavor":
					controllerReason, nodeReason = "ReporterStale", "EvidenceChanged"
				case "unconfigured flavor":
					controllerReason, nodeReason = "SecurityEvidenceUnknown", "SecurityEvidenceUnknown"
				}
				if computed.mountAdmission != "Denied" || computed.reason != controllerReason {
					t.Fatalf("controller must deny for %s: %+v", controllerReason, computed)
				}
				// Even an incorrectly Allowed controller decision with the exact
				// policy fingerprint cannot skip the node's own image approval.
				computed.mountAdmission = "Allowed"
				if err := driver.writeNodeStatus(t.Context(), computed, nil); err != nil {
					t.Fatal(err)
				}
				if err := driver.checkNodeMountAdmission(t.Context()); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), nodeReason) {
					t.Fatalf("mount boundary must deny for %s: %v", nodeReason, err)
				}
			})
		}
	}
}

func TestParseCompatibilityPolicyFlavorImageValidation(t *testing.T) {
	for _, scenario := range []string{"unconfigured OS", "missing loader", "unapproved desired", "unknown flavor", "malformed digest", "digest suffix", "uppercase digest"} {
		t.Run(scenario, func(t *testing.T) {
			policy, _ := testMixedFlavorImagePolicy(t)
			switch scenario {
			case "unconfigured OS":
				policy.Clients["azurelinux3"] = policy.Clients["noble"]
				policy.Images.Driver["azurelinux3"] = compatibilityPolicyImage{}
				policy.Images.Loader["azurelinux3"] = compatibilityPolicyImage{}
			case "missing loader":
				delete(policy.Images.Loader, "noble")
			case "unapproved desired":
				image := policy.Images.Driver["noble"]
				image.ApprovedDigests = image.ApprovedDigests[1:]
				policy.Images.Driver["noble"] = image
			case "unknown flavor":
				policy.Images.Driver["typo"] = policy.Images.Driver["noble"]
			case "malformed digest":
				image := policy.Images.Driver["noble"]
				image.DesiredDigest = "not-an-image-digest"
				policy.Images.Driver["noble"] = image
			case "digest suffix":
				image := policy.Images.Driver["noble"]
				image.DesiredDigest += "-not-part-of-a-digest"
				policy.Images.Driver["noble"] = image
			case "uppercase digest":
				image := policy.Images.Driver["noble"]
				image.DesiredDigest = "sha256:" + strings.Repeat("B", 64)
				image.ApprovedDigests = []string{image.DesiredDigest}
				policy.Images.Driver["noble"] = image
			}
			raw, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseCompatibilityPolicy(raw)
			valid := scenario == "unconfigured OS" || scenario == "uppercase digest"
			if valid && err != nil {
				t.Fatalf("valid per-flavor policy rejected: %v", err)
			}
			if !valid && err == nil {
				t.Fatal("invalid flavor image policy was accepted")
			}
			if scenario == "uppercase digest" && parsed.Images.Driver["noble"].DesiredDigest != "sha256:"+strings.Repeat("b", 64) {
				t.Fatal("per-flavor driver digest was not normalized")
			}
		})
	}
	t.Run("legacy global driver shape", func(t *testing.T) {
		_, raw := testMixedFlavorImagePolicy(t)
		var document map[string]json.RawMessage
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		document["images"] = json.RawMessage(`{"driver":{"desiredDigest":"","approvedDigests":[]},"loader":{}}`)
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseCompatibilityPolicy(raw); err == nil {
			t.Fatal("old global driver policy silently accepted")
		}
	})
}

func testMixedFlavorImagePolicy(t *testing.T) (*compatibilityPolicy, []byte) {
	t.Helper()
	policy := &compatibilityPolicy{
		APIVersion: "azurelustre.csi.azure.com/v1alpha1", Kind: "AzureLustreCompatibilityPolicy",
		ClientSetRevision: 1, CacheTTLSeconds: 3600, MountAdmission: compatibilityPolicyAdmission{Enforce: true},
		Clients: map[string]compatibilityPolicyOS{
			"jammy": {Desired: clientIdentity{Version: "2.15.8", ShaSuffix: "jammy"}},
			"noble": {Desired: clientIdentity{Version: "2.17.0", ShaSuffix: "noble"}},
		},
		Images: compatibilityPolicyImages{
			Driver: map[string]compatibilityPolicyImage{},
			Loader: map[string]compatibilityPolicyImage{},
		},
	}
	for flavor, hex := range map[string]string{"jammy": "a", "noble": "b"} {
		current, prior := "sha256:"+strings.Repeat(hex, 64), "sha256:"+strings.Repeat(hex, 63)+"0"
		images := compatibilityPolicyImage{DesiredDigest: current, ApprovedDigests: []string{current, prior}}
		policy.Images.Driver[flavor], policy.Images.Loader[flavor] = images, images
	}
	return marshalTestImagePolicy(t, policy)
}

func marshalTestImagePolicy(t *testing.T, policy *compatibilityPolicy) (*compatibilityPolicy, []byte) {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseCompatibilityPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed, raw
}

type flavorImageFixture struct {
	node      *corev1.Node
	pod       *corev1.Pod
	lease     *coordinationv1.Lease
	daemonset *appsv1.DaemonSet
	revision  *appsv1.ControllerRevision
}

func newFlavorImageFixture(t *testing.T, namespace, flavor string, policy *compatibilityPolicy) *flavorImageFixture {
	t.Helper()
	node, pod, lease, daemonset, revision := testNodeStatusObjects(namespace)
	node.Name, node.UID = flavor+"-node", types.UID(flavor+"-node-uid")
	osSKU, osImage, kernel := "Ubuntu2204", "Ubuntu 22.04", "5.15.0-1121-azure"
	if flavor == "noble" {
		osSKU, osImage, kernel = "Ubuntu2404", "Ubuntu 24.04", "6.8.0-test-azure"
	}
	node.Labels = map[string]string{"kubernetes.io/os": "linux", "kubernetes.azure.com/os-sku-effective": osSKU}
	node.Status.NodeInfo.OSImage, node.Status.NodeInfo.KernelVersion, node.Status.NodeInfo.BootID = osImage, kernel, flavor+"-boot"
	pod.Name, pod.UID, pod.Spec.NodeName = flavor+"-pod", types.UID(flavor+"-pod-uid"), node.Name
	pod.Labels["flavor"] = flavor
	pod.Spec.Containers = []corev1.Container{{Name: "azurelustre", Image: "example.invalid/azurelustre:current-" + flavor}}
	pod.Spec.InitContainers = []corev1.Container{{Name: "lustre-loader", Image: "example.invalid/azurelustre:current-" + flavor}}
	pod.Spec.NodeSelector = node.Labels
	daemonset.Name, daemonset.UID, daemonset.Labels["flavor"] = flavor+"-ds", types.UID(flavor+"-ds-uid"), flavor
	daemonset.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "csi-azurelustre-node", "flavor": flavor}},
		Spec:       *pod.Spec.DeepCopy(),
	}
	daemonset.Spec.Template.Spec.NodeName = ""
	pod.OwnerReferences[0].Name, pod.OwnerReferences[0].UID = daemonset.Name, daemonset.UID
	revision.Name = daemonset.Name + "-desired-hash"
	revision.OwnerReferences[0] = pod.OwnerReferences[0]
	rawTemplate, err := json.Marshal(map[string]interface{}{"spec": map[string]interface{}{"template": daemonset.Spec.Template}})
	if err != nil {
		t.Fatal(err)
	}
	revision.Data.Raw = rawTemplate
	lease.Name = flavor + "-fact"
	lease.Spec.HolderIdentity = ptr(string(pod.UID))
	for field, value := range map[string]string{
		"node-name": node.Name, "node-uid": string(node.UID), "pod-name": pod.Name,
		"pod-uid": string(pod.UID), "flavor": flavor,
		"boot-id": node.Status.NodeInfo.BootID, "kernel-version": kernel,
		"loaded-client": desiredClientIdentity(policy.Clients[flavor].Desired.Version, policy.Clients[flavor].Desired.ShaSuffix),
	} {
		lease.Annotations[nodeFactAnnotationPrefix+field] = value
	}
	fixture := &flavorImageFixture{node: node, pod: pod, lease: lease, daemonset: daemonset, revision: revision}
	fixture.setImageDigests(policy.Images.Driver[flavor].DesiredDigest, policy.Images.Loader[flavor].DesiredDigest)
	return fixture
}

func (f *flavorImageFixture) setImageDigests(driver, loader string) {
	f.pod.Status.ContainerStatuses[0].ImageID = "docker-pullable://driver@" + driver
	f.pod.Status.InitContainerStatuses[0].ImageID = "docker-pullable://driver@" + loader
	f.lease.Annotations[nodeFactAnnotationPrefix+"driver-image-id"] = f.pod.Status.ContainerStatuses[0].ImageID
	f.lease.Annotations[nodeFactAnnotationPrefix+"loader-image-id"] = f.pod.Status.InitContainerStatuses[0].ImageID
}

func (f *flavorImageFixture) admissionDriver(raw []byte) *Driver {
	return &Driver{
		CSIDriver: CSIDriver{NodeID: f.node.Name}, podNamespace: f.pod.Namespace, podName: f.pod.Name,
		kubeClient: fake.NewSimpleClientset(f.node, f.pod),
		dynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"}),
		mountAdmissionPolicyEnabled: true, mountAdmissionPolicyPath: "policy",
		mountAdmissionReadFile: func(string) ([]byte, error) { return raw, nil },
		nodeFactReadFile: func(path string) ([]byte, error) {
			switch path {
			case "/proc/sys/kernel/random/boot_id":
				return []byte(f.node.Status.NodeInfo.BootID), nil
			case "/proc/sys/kernel/osrelease":
				return []byte(f.node.Status.NodeInfo.KernelVersion), nil
			case "/sys/module/lustre/version":
				return []byte(f.lease.Annotations[nodeFactAnnotationPrefix+"loaded-client"]), nil
			default:
				return nil, os.ErrNotExist
			}
		},
	}
}
