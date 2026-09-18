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
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func testNodeFactObjects() (*corev1.Node, *corev1.Pod) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
			UID:  types.UID("node-uid-1"),
		},
		Spec: corev1.NodeSpec{ProviderID: "azure:///subscriptions/test/node-1"},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node-pod-1",
			Namespace: "kube-system",
			UID:       types.UID("pod-uid-1"),
			Labels: map[string]string{
				"controller-revision-hash": "revision-1",
				"flavor":                   "noble",
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "azurelustre",
					ImageID:      "driver@sha256:driver",
					ContainerID:  "containerd://driver",
					RestartCount: 2,
				},
			},
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "lustre-loader",
					ImageID:      "driver@sha256:loader",
					ContainerID:  "containerd://loader",
					RestartCount: 1,
				},
			},
		},
	}
	return node, pod
}

func TestPublishNodeFact(t *testing.T) {
	node, pod := testNodeFactObjects()
	client := fake.NewSimpleClientset(node, pod)
	files := map[string]string{
		"/proc/sys/kernel/random/boot_id": "boot-id-1\n",
		"/proc/sys/kernel/osrelease":      "6.6.1-test\n",
		"/sys/module/lustre/version":      "2.17.0_24_gf517bc4\n",
	}
	driver := &Driver{
		CSIDriver:             CSIDriver{NodeID: node.Name, Version: "v0.7.0"},
		podRole:               nodePod,
		kubeClient:            client,
		podName:               pod.Name,
		podNamespace:          pod.Namespace,
		nodeFactDesiredClient: "2.17.0_24_gf517bc4",
		nodeFactReadFile: func(path string) ([]byte, error) {
			value, ok := files[path]
			if !ok {
				return nil, errors.New("unexpected path")
			}
			return []byte(value), nil
		},
	}

	err := driver.publishNodeFact(context.Background())
	require.NoError(t, err)

	leaseName := nodeFactLeaseName(node.Name, string(node.UID))
	lease, err := client.CoordinationV1().Leases(pod.Namespace).Get(
		context.Background(), leaseName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, lease.Spec.RenewTime)
	assert.WithinDuration(t, time.Now(), lease.Spec.RenewTime.Time, time.Second)
	assert.Equal(t, "pod-uid-1", *lease.Spec.HolderIdentity)
	assert.Equal(t, nodeFactLeaseDuration, *lease.Spec.LeaseDurationSeconds)

	expectedAnnotations := map[string]string{
		nodeFactAnnotationPrefix + "schema-version":       nodeFactSchemaVersion,
		nodeFactAnnotationPrefix + "node-name":            "node-1",
		nodeFactAnnotationPrefix + "node-uid":             "node-uid-1",
		nodeFactAnnotationPrefix + "provider-id":          "azure:///subscriptions/test/node-1",
		nodeFactAnnotationPrefix + "boot-id":              "boot-id-1",
		nodeFactAnnotationPrefix + "pod-name":             "node-pod-1",
		nodeFactAnnotationPrefix + "pod-uid":              "pod-uid-1",
		nodeFactAnnotationPrefix + "pod-revision":         "revision-1",
		nodeFactAnnotationPrefix + "flavor":               "noble",
		nodeFactAnnotationPrefix + "kernel-version":       "6.6.1-test",
		nodeFactAnnotationPrefix + "desired-client":       "2.17.0_24_gf517bc4",
		nodeFactAnnotationPrefix + "loaded-client":        "2.17.0_24_gf517bc4",
		nodeFactAnnotationPrefix + "driver-image-id":      "driver@sha256:driver",
		nodeFactAnnotationPrefix + "loader-image-id":      "driver@sha256:loader",
		nodeFactAnnotationPrefix + "driver-container-id":  "containerd://driver",
		nodeFactAnnotationPrefix + "loader-container-id":  "containerd://loader",
		nodeFactAnnotationPrefix + "driver-restart-count": "2",
		nodeFactAnnotationPrefix + "loader-restart-count": "1",
		nodeFactAnnotationPrefix + "reporter-version":     "v0.7.0",
	}
	assert.Equal(t, expectedAnnotations, lease.Annotations)
}

func TestPublishNodeFactRequiresReporterPod(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
			UID:  types.UID("node-uid-1"),
		},
	}
	driver := &Driver{
		CSIDriver:    CSIDriver{NodeID: node.Name},
		podRole:      nodePod,
		kubeClient:   fake.NewSimpleClientset(node),
		podName:      "missing-pod",
		podNamespace: "kube-system",
	}

	err := driver.publishNodeFact(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "get reporter pod")
	leases, listErr := driver.kubeClient.CoordinationV1().Leases("kube-system").List(
		context.Background(), metav1.ListOptions{})
	require.NoError(t, listErr)
	assert.Empty(t, leases.Items)
}

func TestDesiredClientIdentity(t *testing.T) {
	assert.Equal(t, "2.17.0_24_gf517bc4", desiredClientIdentity(" 2.17.0 ", "24-gf517bc4 "))
	assert.Empty(t, desiredClientIdentity("", "24-gf517bc4"))
	assert.Empty(t, desiredClientIdentity("2.17.0", ""))
}

func TestPublishNodeFactReplacesPreviousEvidence(t *testing.T) {
	node, pod := testNodeFactObjects()
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeFactLeaseName(node.Name, string(node.UID)),
			Namespace: pod.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/component": "node-fact"},
			Annotations: map[string]string{
				nodeFactAnnotationPrefix + "desired-client":      "previous-client",
				nodeFactAnnotationPrefix + "loaded-client":       "previous-client",
				nodeFactAnnotationPrefix + "boot-id":             "previous-boot",
				nodeFactAnnotationPrefix + "loader-container-id": "previous-container",
				nodeFactAnnotationPrefix + "loader-image-id":     "previous-image",
			},
		},
		Spec: coordinationv1.LeaseSpec{
			RenewTime:      &metav1.MicroTime{Time: time.Now().Add(-time.Hour)},
			HolderIdentity: ptr("previous-pod"),
		},
	}
	pod.Status.InitContainerStatuses = nil
	client := fake.NewSimpleClientset(node, pod, lease)
	driver := &Driver{
		CSIDriver: CSIDriver{NodeID: node.Name}, kubeClient: client,
		podName: pod.Name, podNamespace: pod.Namespace,
	}

	require.NoError(t, driver.publishNodeFact(t.Context()))
	updated, err := client.CoordinationV1().Leases(pod.Namespace).Get(t.Context(), lease.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, string(pod.UID), *updated.Spec.HolderIdentity)
	assert.Equal(t, nodeFactLeaseDuration, *updated.Spec.LeaseDurationSeconds)
	assert.WithinDuration(t, time.Now(), updated.Spec.RenewTime.Time, time.Second)
	assert.Equal(t, lease.Labels, updated.Labels)
	assert.Equal(t, pod.Status.ContainerStatuses[0].ContainerID, updated.Annotations[nodeFactAnnotationPrefix+"driver-container-id"])
	for _, field := range []string{"loaded-client", "desired-client", "boot-id", "loader-container-id", "loader-image-id"} {
		assert.NotContains(t, updated.Annotations, nodeFactAnnotationPrefix+field, "unavailable evidence must not survive a renewal")
	}
}

func TestPublishNodeFactPropagatesAPIFailures(t *testing.T) {
	for _, test := range []struct {
		name, verb, resource, message string
		existingLease                 bool
	}{
		{"node lookup", "get", "nodes", "get node", false},
		{"lease lookup", "get", "leases", "write node fact lease", false},
		{"lease creation", "create", "leases", "write node fact lease", false},
		{"lease renewal", "update", "leases", "write node fact lease", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			node, pod := testNodeFactObjects()
			objects := []runtime.Object{node, pod}
			if test.existingLease {
				objects = append(objects, &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
					Name: nodeFactLeaseName(node.Name, string(node.UID)), Namespace: pod.Namespace,
				}})
			}

			client := fake.NewSimpleClientset(objects...)
			failure := errors.New("injected API failure")
			client.PrependReactor(test.verb, test.resource, func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, failure
			})
			driver := &Driver{
				CSIDriver: CSIDriver{NodeID: node.Name}, kubeClient: client,
				podName: pod.Name, podNamespace: pod.Namespace,
			}
			err := driver.publishNodeFact(t.Context())
			require.ErrorIs(t, err, failure)
			assert.ErrorContains(t, err, test.message)
		})
	}
}

func TestNodeFactReporterRequiresNodeIdentity(t *testing.T) {
	for _, field := range []string{"role", "client", "node", "pod", "namespace"} {
		t.Run(field, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			driver := &Driver{
				CSIDriver: CSIDriver{NodeID: "node"}, podRole: nodePod, kubeClient: client,
				podName: "pod", podNamespace: "driver",
			}
			switch field {
			case "role":
				driver.podRole = controllerPod
			case "client":
				driver.kubeClient = nil
			case "node":
				driver.NodeID = ""
			case "pod":
				driver.podName = ""
			case "namespace":
				driver.podNamespace = ""
			}
			driver.startNodeFactReporter()
			assert.Empty(t, client.Actions(), "invalid reporter identity must not publish facts")
		})
	}
}
