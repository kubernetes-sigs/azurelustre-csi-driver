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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	nodeFactAnnotationPrefix = "azurelustre.csi.azure.com/"
	nodeFactSchemaVersion    = "v1alpha1"
	nodeFactLeasePrefix      = "azurelustre-node-fact-"
	nodeFactLeaseDuration    = int32(90)
	defaultNodeFactInterval  = 30 * time.Second
)

type nodeFactSnapshot struct {
	nodeUID            string
	providerID         string
	bootID             string
	podName            string
	podUID             string
	podRevision        string
	flavor             string
	kernelVersion      string
	desiredClient      string
	loadedClient       string
	driverImageID      string
	loaderImageID      string
	driverContainerID  string
	loaderContainerID  string
	driverRestartCount int32
	loaderRestartCount int32
}

func (d *Driver) startNodeFactReporter() {
	if d.podRole != nodePod || d.kubeClient == nil || d.NodeID == "" {
		return
	}
	if d.podName == "" || d.podNamespace == "" {
		klog.Warning("node fact reporter disabled because POD_NAME or POD_NAMESPACE is unset")
		return
	}

	interval := d.nodeFactInterval
	if interval <= 0 {
		interval = defaultNodeFactInterval
	}
	go func() {
		d.publishNodeFactWithTimeout()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			d.publishNodeFactWithTimeout()
		}
	}()
}

func (d *Driver) publishNodeFactWithTimeout() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.publishNodeFact(ctx); err != nil {
		klog.Errorf("failed to publish Azure Lustre node fact for %s: %v", d.NodeID, err)
	}
}

func (d *Driver) publishNodeFact(ctx context.Context) error {
	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, d.NodeID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get node %q: %w", d.NodeID, err)
	}
	pod, err := d.kubeClient.CoreV1().Pods(d.podNamespace).Get(ctx, d.podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get reporter pod %q: %w", d.podName, err)
	}

	fact := d.collectNodeFact(node, pod)
	annotations := map[string]string{
		nodeFactAnnotationPrefix + "schema-version":       nodeFactSchemaVersion,
		nodeFactAnnotationPrefix + "node-name":            d.NodeID,
		nodeFactAnnotationPrefix + "node-uid":             fact.nodeUID,
		nodeFactAnnotationPrefix + "provider-id":          fact.providerID,
		nodeFactAnnotationPrefix + "boot-id":              fact.bootID,
		nodeFactAnnotationPrefix + "pod-name":             fact.podName,
		nodeFactAnnotationPrefix + "pod-uid":              fact.podUID,
		nodeFactAnnotationPrefix + "pod-revision":         fact.podRevision,
		nodeFactAnnotationPrefix + "flavor":               fact.flavor,
		nodeFactAnnotationPrefix + "kernel-version":       fact.kernelVersion,
		nodeFactAnnotationPrefix + "desired-client":       fact.desiredClient,
		nodeFactAnnotationPrefix + "loaded-client":        fact.loadedClient,
		nodeFactAnnotationPrefix + "driver-image-id":      fact.driverImageID,
		nodeFactAnnotationPrefix + "loader-image-id":      fact.loaderImageID,
		nodeFactAnnotationPrefix + "driver-container-id":  fact.driverContainerID,
		nodeFactAnnotationPrefix + "loader-container-id":  fact.loaderContainerID,
		nodeFactAnnotationPrefix + "driver-restart-count": strconv.FormatInt(int64(fact.driverRestartCount), 10),
		nodeFactAnnotationPrefix + "loader-restart-count": strconv.FormatInt(int64(fact.loaderRestartCount), 10),
		nodeFactAnnotationPrefix + "reporter-version":     d.Version,
	}
	for key, value := range annotations {
		if value == "" {
			delete(annotations, key)
		}
	}

	now := metav1.NewMicroTime(time.Now().UTC())
	holderIdentity := string(pod.UID)
	leaseName := nodeFactLeaseName(d.NodeID, string(node.UID))
	leases := d.kubeClient.CoordinationV1().Leases(d.podNamespace)
	lease, err := leases.Get(ctx, leaseName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = leases.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:        leaseName,
				Namespace:   d.podNamespace,
				Annotations: annotations,
				Labels: map[string]string{
					"app.kubernetes.io/name":      "azurelustre-csi-driver",
					"app.kubernetes.io/component": "node-fact",
				},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holderIdentity,
				LeaseDurationSeconds: ptr(nodeFactLeaseDuration),
				RenewTime:            &now,
			},
		}, metav1.CreateOptions{})
	} else if err == nil {
		lease.Annotations = annotations
		lease.Spec.HolderIdentity = &holderIdentity
		lease.Spec.LeaseDurationSeconds = ptr(nodeFactLeaseDuration)
		lease.Spec.RenewTime = &now
		_, err = leases.Update(ctx, lease, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("write node fact lease %q: %w", leaseName, err)
	}
	return nil
}

func (d *Driver) collectNodeFact(node *corev1.Node, pod *corev1.Pod) nodeFactSnapshot {
	driverStatus := findContainerStatus(pod.Status.ContainerStatuses, "azurelustre")
	loaderStatus := findContainerStatus(pod.Status.InitContainerStatuses, "lustre-loader")
	return nodeFactSnapshot{
		nodeUID:            string(node.UID),
		providerID:         node.Spec.ProviderID,
		bootID:             d.readNodeFactFile("/proc/sys/kernel/random/boot_id"),
		podName:            pod.Name,
		podUID:             string(pod.UID),
		podRevision:        pod.Labels["controller-revision-hash"],
		flavor:             pod.Labels["flavor"],
		kernelVersion:      d.readNodeFactFile("/proc/sys/kernel/osrelease"),
		desiredClient:      d.nodeFactDesiredClient,
		loadedClient:       d.readNodeFactFile("/sys/module/lustre/version"),
		driverImageID:      driverStatus.ImageID,
		loaderImageID:      loaderStatus.ImageID,
		driverContainerID:  driverStatus.ContainerID,
		loaderContainerID:  loaderStatus.ContainerID,
		driverRestartCount: driverStatus.RestartCount,
		loaderRestartCount: loaderStatus.RestartCount,
	}
}

func (d *Driver) readNodeFactFile(path string) string {
	if d.nodeFactReadFile == nil {
		return ""
	}
	value, err := d.nodeFactReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(value))
}

func desiredClientIdentity(version, sha string) string {
	version = strings.TrimSpace(version)
	sha = strings.TrimSpace(sha)
	if version == "" || sha == "" {
		return ""
	}
	return version + "_" + strings.ReplaceAll(sha, "-", "_")
}

func findContainerStatus(statuses []corev1.ContainerStatus, name string) corev1.ContainerStatus {
	for _, status := range statuses {
		if status.Name == name {
			return status
		}
	}
	return corev1.ContainerStatus{}
}

func nodeFactLeaseName(nodeName, nodeUID string) string {
	sum := sha256.Sum256([]byte(nodeName + "\x00" + nodeUID))
	return nodeFactLeasePrefix + hex.EncodeToString(sum[:10])
}

func ptr[T any](value T) *T {
	return &value
}
