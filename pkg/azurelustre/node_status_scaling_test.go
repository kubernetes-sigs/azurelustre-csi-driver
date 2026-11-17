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
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestReconcileNodeStatusesUsesOneStatusListAtScale(t *testing.T) {
	const size = 1000 // API action-count regression, not a supported fleet-size claim.
	namespace := "driver"
	nodes := make([]runtime.Object, 0, size+1)
	statuses := make([]runtime.Object, 0, size)
	for i := range size {
		name := fmt.Sprintf("node-%04d", i)
		nodes = append(nodes, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name)}})
		statuses = append(statuses, testStatusSnapshot(name, namespace, name))
	}
	nodes = append(nodes, &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "driver", Namespace: namespace, Labels: map[string]string{
			"app": "csi-azurelustre-node",
		}},
	})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"}, statuses...)
	driver := &Driver{kubeClient: fake.NewSimpleClientset(nodes...), dynamicClient: client, podNamespace: namespace}
	if err := driver.reconcileNodeStatuses(context.Background()); err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, action := range client.Actions() {
		counts[action.GetVerb()+"/"+action.GetSubresource()]++
	}
	if counts["list/"] != 1 || counts["update/status"] != size || len(counts) != 2 {
		t.Fatalf("unexpected dynamic API budget for %d existing nodes: %v", size, counts)
	}
}

func TestNodeStatusJobsMakeFairProgressAcrossCancelledSweeps(t *testing.T) {
	const size = 257
	driver := &Driver{}
	var mu sync.Mutex
	seen := make(map[string]bool)
	for sweep := 0; sweep < (size+7)/8; sweep++ {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{}, 8)
		jobs := make([]nodeStatusJob, size)
		for i := range size {
			name := fmt.Sprintf("node-%04d", i)
			jobs[i] = nodeStatusJob{name: name, run: func(ctx context.Context) error {
				mu.Lock()
				seen[name] = true
				mu.Unlock()
				started <- struct{}{}
				<-ctx.Done()
				return errors.New("injected slow node failure")
			}}
		}
		done := make(chan error, 1)
		go func() { done <- driver.runNodeStatusJobs(ctx, jobs) }()
		// Eight in-flight failures exhaust this sweep. Cancellation is triggered
		// by a barrier rather than timing assumptions or fake-client latency.
		for range 8 {
			awaitStatusSignal(t, started)
		}
		cancel()
		if err := awaitStatusResult(t, done); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled sweep error = %v", err)
		}
	}
	if len(seen) != size {
		t.Fatalf("fair sweeps attempted %d of %d jobs", len(seen), size)
	}
	cursor := driver.statusControllerCursor
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := driver.runNodeStatusJobs(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("empty cancelled sweep error = %v", err)
	}
	if driver.statusControllerCursor != cursor {
		t.Fatal("empty sweep reset fairness cursor")
	}
}

func TestNodeStatusJobsCursorSurvivesMembershipChange(t *testing.T) {
	driver := &Driver{statusControllerCursor: "node-2"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	seen := make(map[string]bool)
	jobs := []nodeStatusJob{}
	for _, name := range []string{"node-1", "node-3", "node-4"} {
		jobs = append(jobs, nodeStatusJob{name: name, run: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			seen[name] = true
			return nil
		}})
	}
	if err := driver.runNodeStatusJobs(ctx, jobs); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || driver.statusControllerCursor != "node-1" {
		t.Fatalf("membership change lost progress: seen=%v cursor=%q", seen, driver.statusControllerCursor)
	}
}

func TestWriteNodeStatusRefreshesOnlyOnConflict(t *testing.T) {
	snapshot := testStatusSnapshot("node", "driver", "node-uid")
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), snapshot.DeepCopy())
	conflicts := 0
	client.PrependReactor("update", "azurelustrenodestatuses", func(action clienttesting.Action) (bool, runtime.Object, error) {
		updateAction, ok := action.(clienttesting.UpdateAction)
		if !ok {
			t.Fatalf("unexpected update action %T", action)
		}
		object, ok := updateAction.GetObject().(*unstructured.Unstructured)
		if !ok {
			t.Fatalf("unexpected update object %T", updateAction.GetObject())
		}
		if conflicts == 0 {
			if object.GetResourceVersion() != "7" {
				t.Errorf("writer discarded snapshot resourceVersion: %q", object.GetResourceVersion())
			}
			conflicts++
			concurrent := snapshot.DeepCopy()
			concurrent.SetResourceVersion("8")
			if err := client.Tracker().Update(nodeStatusGVR, concurrent, "driver"); err != nil {
				t.Fatal(err)
			}
			return true, nil, apierrors.NewConflict(nodeStatusGVR.GroupResource(), "node", errors.New("injected conflict"))
		}
		if object.GetResourceVersion() != "8" {
			t.Errorf("writer did not refresh resourceVersion after conflict: %q", object.GetResourceVersion())
		}
		return false, nil, nil
	})
	driver := &Driver{dynamicClient: client, podNamespace: "driver"}
	if err := driver.writeNodeStatus(context.Background(), computedNodeStatus{nodeName: "node", nodeUID: "node-uid"}, snapshot); err != nil {
		t.Fatal(err)
	}
	var gets, updates int
	for _, action := range client.Actions() {
		switch action.GetVerb() {
		case "get":
			gets++
		case "update":
			updates++
		}
	}
	if gets != 1 || updates != 2 {
		t.Fatalf("conflict retry calls: GET=%d UPDATE=%d", gets, updates)
	}
	if _, found, err := unstructured.NestedMap(snapshot.Object, "status"); err != nil || found {
		t.Fatalf("writer mutated the shared LIST snapshot: %v", err)
	}
}

func TestWriteNodeStatusCreateRace(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), testStatusSnapshot("node", "driver", "old-uid"))
	driver := &Driver{dynamicClient: client, podNamespace: "driver"}
	if err := driver.writeNodeStatus(context.Background(), computedNodeStatus{nodeName: "node", nodeUID: "new-uid"}, nil); err != nil {
		t.Fatal(err)
	}
	object, err := client.Resource(nodeStatusGVR).Namespace("driver").Get(context.Background(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if uid, _, err := unstructured.NestedString(object.Object, "spec", "nodeUID"); err != nil || uid != "new-uid" {
		t.Fatalf("node recreation did not refresh spec: %q, %v", uid, err)
	}
}

func TestStatusGarbageCollectionPreservesPreconditionsAndIndependentErrors(t *testing.T) {
	namespace := "driver"
	broken := testStatusSnapshot("orphan-0", namespace, "old-node")
	orphan := testStatusSnapshot("orphan-1", namespace, "old-node")
	duplicate := testStatusSnapshot("duplicate", namespace, "live")
	duplicate.Object["spec"] = map[string]interface{}{"nodeName": "live", "nodeUID": "live"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"}, broken, orphan, duplicate)
	client.PrependReactor("delete", "azurelustrenodestatuses", func(action clienttesting.Action) (bool, runtime.Object, error) {
		deleteAction, ok := action.(clienttesting.DeleteAction)
		if !ok {
			t.Fatalf("unexpected delete action %T", action)
		}
		preconditions := deleteAction.GetDeleteOptions().Preconditions
		if preconditions == nil || preconditions.UID == nil || *preconditions.UID != types.UID("status-"+deleteAction.GetName()) ||
			preconditions.ResourceVersion == nil || *preconditions.ResourceVersion != "7" {
			t.Errorf("unsafe delete options: %#v", preconditions)
		}
		if deleteAction.GetName() == broken.GetName() {
			return true, nil, apierrors.NewConflict(nodeStatusGVR.GroupResource(), broken.GetName(), errors.New("concurrent update"))
		}
		return false, nil, nil
	})
	driver := &Driver{
		kubeClient: fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: "live", UID: "live",
		}}),
		dynamicClient: client, podNamespace: namespace,
	}
	if err := driver.reconcileNodeStatuses(context.Background()); !apierrors.IsConflict(err) {
		t.Fatalf("expected independent GC conflict, got %v", err)
	}
	for _, name := range []string{"orphan-1", "duplicate"} {
		if _, err := client.Resource(nodeStatusGVR).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("orphan %q was not deleted independently: %v", name, err)
		}
	}
}

func TestReconcileNodeStatusRecreationDoesNotGarbageCollectRefreshedSnapshot(t *testing.T) {
	node, pod, lease, daemonset, revision := testNodeStatusObjects("driver")
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{nodeStatusGVR: "AzureLustreNodeStatusList"},
		testStatusSnapshot(node.Name, "driver", "previous-uid"))
	driver := &Driver{kubeClient: fake.NewSimpleClientset(node, pod, lease, daemonset, revision), dynamicClient: client, podNamespace: "driver"}
	if err := driver.reconcileNodeStatuses(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("recreated live node's refreshed status was garbage collected")
		}
	}
}

func testStatusSnapshot(name, namespace, nodeUID string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "azurelustre.csi.azure.com/v1alpha1",
		"kind":       "AzureLustreNodeStatus",
		"metadata": map[string]interface{}{
			"name": name, "namespace": namespace, "uid": "status-" + name, "resourceVersion": "7",
		},
		"spec": map[string]interface{}{"nodeName": name, "nodeUID": nodeUID},
	}}
	return object
}
