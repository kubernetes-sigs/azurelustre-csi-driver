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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

const (
	defaultStatusControllerInterval = 30 * time.Second
	statusControllerLeaseName       = "azurelustre-node-status-controller"
	compatibilityPolicyKey          = "policy.yaml"
	mountAdmissionAnnotation        = "azurelustre.csi.azure.com/mount-admission"
)

var nodeStatusGVR = schema.GroupVersionResource{
	Group:    "azurelustre.csi.azure.com",
	Version:  "v1alpha1",
	Resource: "azurelustrenodestatuses",
}

type compatibilityPolicy struct {
	APIVersion        string                           `json:"apiVersion"`
	Kind              string                           `json:"kind"`
	ClientSetRevision int64                            `json:"clientSetRevision"`
	CacheTTLSeconds   int64                            `json:"cacheTTLSeconds"`
	MountAdmission    compatibilityPolicyAdmission     `json:"mountAdmission"`
	Images            compatibilityPolicyImages        `json:"images"`
	Clients           map[string]compatibilityPolicyOS `json:"clients"`
	Bootstrap         compatibilityPolicyBootstrap     `json:"bootstrap"`
	fingerprint       string
}

type compatibilityPolicyOS struct {
	Desired    clientIdentity   `json:"desired"`
	Compatible []clientIdentity `json:"compatible"`
}

type clientIdentity struct {
	Version   string `json:"version"`
	ShaSuffix string `json:"shaSuffix"`
}

type compatibilityPolicyBootstrap struct {
	Enabled   bool                        `json:"enabled"`
	ExpiresAt string                      `json:"expiresAt"`
	CSIImage  compatibilityPolicyCSIImage `json:"csiImage"`
}

type compatibilityPolicyCSIImage struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
}

type compatibilityPolicyAdmission struct {
	Enforce bool `json:"enforce"`
}

type compatibilityPolicyImages struct {
	Driver map[string]compatibilityPolicyImage `json:"driver"`
	Loader map[string]compatibilityPolicyImage `json:"loader"`
}

type compatibilityPolicyImage struct {
	DesiredDigest   string   `json:"desiredDigest"`
	ApprovedDigests []string `json:"approvedDigests"`
}

type computedNodeStatus struct {
	nodeName            string
	nodeUID             string
	schedulingCoverage  string
	reporterFreshness   string
	csiDelivery         string
	clientCompatibility string
	kernelCoverage      string
	securityCompliance  string
	mountAdmission      string
	reason              string
	message             string
	desired             map[string]interface{}
	observed            map[string]interface{}
	activation          map[string]interface{}
}

func (d *Driver) reconcileNodeStatuses(ctx context.Context) error {
	policy, policyErr := d.loadCompatibilityPolicy(ctx)
	nodes, err := d.kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list nodes for Azure Lustre status: %w", err)
	}
	pods, err := d.kubeClient.CoreV1().Pods(d.podNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=csi-azurelustre-node",
	})
	if err != nil {
		return fmt.Errorf("list Azure Lustre node pods: %w", err)
	}
	leases, err := d.kubeClient.CoordinationV1().Leases(d.podNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=node-fact",
	})
	if err != nil {
		return fmt.Errorf("list Azure Lustre node facts: %w", err)
	}
	daemonsets, err := d.kubeClient.AppsV1().DaemonSets(d.podNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=csi-azurelustre-node",
	})
	if err != nil {
		return fmt.Errorf("list Azure Lustre node DaemonSets: %w", err)
	}
	revisions, err := d.kubeClient.AppsV1().ControllerRevisions(d.podNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list Azure Lustre ControllerRevisions: %w", err)
	}
	statuses, err := d.dynamicClient.Resource(nodeStatusGVR).Namespace(d.podNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list Azure Lustre node statuses: %w", err)
	}
	statusesByName := make(map[string]*unstructured.Unstructured, len(statuses.Items))
	for i := range statuses.Items {
		statusesByName[statuses.Items[i].GetName()] = &statuses.Items[i]
	}

	nodesByName := make(map[string]*corev1.Node, len(nodes.Items))
	for i := range nodes.Items {
		nodesByName[nodes.Items[i].Name] = &nodes.Items[i]
	}
	desiredRevisions := desiredDaemonSetRevisions(daemonsets.Items, revisions.Items)
	factsByNode := newestFactsByNode(leases.Items)
	observedNodes := make(map[string]struct{}, len(pods.Items))
	pendingByNode := make(map[string]int, len(pods.Items))
	pending := make([]computedNodeStatus, 0, len(nodes.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName == "" {
			continue
		}
		if _, seen := observedNodes[pod.Spec.NodeName]; seen {
			// A node with multiple competing driver pods has no unique reporter.
			if i, exists := pendingByNode[pod.Spec.NodeName]; exists {
				pending[i].mountAdmission = "Denied"
				pending[i].reason = "AmbiguousNodePods"
				pending[i].message = "Multiple Azure Lustre node pods are assigned to this node."
			}
			continue
		}
		observedNodes[pod.Spec.NodeName] = struct{}{}
		node := nodesByName[pod.Spec.NodeName]
		if node == nil {
			continue
		}
		status := d.computeNodeStatus(
			node,
			pod,
			factsByNode[pod.Spec.NodeName],
			policy,
			policyErr,
			desiredRevisions,
		)
		pendingByNode[status.nodeName] = len(pending)
		pending = append(pending, status)
	}

	for nodeName, lease := range factsByNode {
		if _, ok := observedNodes[nodeName]; ok {
			continue
		}
		node := nodesByName[nodeName]
		if node == nil {
			continue
		}
		status := staleFactStatus(node, lease)
		pending = append(pending, status)
		observedNodes[nodeName] = struct{}{}
	}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if _, ok := observedNodes[node.Name]; ok {
			continue
		}
		daemonset := matchingNodeDaemonSet(node, daemonsets.Items)
		if daemonset == nil {
			continue
		}
		status := missingNodePodStatus(node, daemonset, policy, policyErr)
		pending = append(pending, status)
	}
	// Include garbage collection in the same fair queue so a fleet that cannot
	// finish a sweep still makes progress deleting orphaned statuses.
	jobs := make([]nodeStatusJob, 0, len(pending)+len(statuses.Items))
	writers := make(map[string]struct{}, len(pending))
	for _, status := range pending {
		writers[status.nodeName] = struct{}{}
		jobs = append(jobs, nodeStatusJob{name: status.nodeName, run: func(ctx context.Context) error {
			return d.writeNodeStatus(ctx, status, statusesByName[status.nodeName])
		}})
	}
	for i := range statuses.Items {
		status := &statuses.Items[i]
		// The writer owns canonical statuses for live nodes, including replacing
		// their old node UID. Never GC the pre-update snapshot of that object.
		if _, writing := writers[status.GetName()]; writing {
			continue
		}
		jobs = append(jobs, nodeStatusJob{name: status.GetName(), run: func(ctx context.Context) error {
			return d.garbageCollectNodeStatus(ctx, status, nodesByName)
		}})
	}
	return d.runNodeStatusJobs(ctx, jobs)
}

type nodeStatusJob struct {
	name string
	run  func(context.Context) error
}

// Reconciliations are serialized by the leader's loop. Advance past every
// started job (even a failed one), not just successful writes, so slow or
// broken nodes cannot monopolize the next bounded sweep.
func (d *Driver) runNodeStatusJobs(ctx context.Context, jobs []nodeStatusJob) error {
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].name < jobs[j].name })
	start := sort.Search(len(jobs), func(i int) bool { return jobs[i].name > d.statusControllerCursor })
	reconcileErrors := make([]error, len(jobs))
	attempted := make([]bool, len(jobs))
	work := make(chan int)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range work {
				if ctx.Err() != nil {
					continue
				}
				attempted[i] = true
				reconcileErrors[i] = jobs[i].run(ctx)
			}
		}()
	}
dispatch:
	for offset := range jobs {
		if ctx.Err() != nil {
			break
		}
		i := (start + offset) % len(jobs)
		select {
		case <-ctx.Done():
			break dispatch
		case work <- i:
		}
	}
	close(work)
	workers.Wait()
	for offset := range jobs {
		i := (start + offset) % len(jobs)
		if !attempted[i] {
			break
		}
		d.statusControllerCursor = jobs[i].name
	}
	return errors.Join(errors.Join(reconcileErrors...), ctx.Err())
}

func (d *Driver) loadCompatibilityPolicy(ctx context.Context) (*compatibilityPolicy, error) {
	configMap, err := d.kubeClient.CoreV1().ConfigMaps(d.podNamespace).Get(
		ctx, d.compatibilityPolicyConfigMap, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			d.compatibilityPolicyMu.Lock()
			d.cachedCompatibilityPolicy = nil
			d.compatibilityPolicyMu.Unlock()
			return nil, fmt.Errorf("compatibility policy is no longer accessible: %w", err)
		}
		if cached := d.currentCachedCompatibilityPolicy(time.Now()); cached != nil {
			return cached, nil
		}
		return nil, fmt.Errorf("get compatibility policy: %w", err)
	}
	raw, ok := configMap.Data[compatibilityPolicyKey]
	if !ok {
		return nil, fmt.Errorf("compatibility policy ConfigMap is missing %q", compatibilityPolicyKey)
	}
	policy, err := parseCompatibilityPolicy([]byte(raw))
	if err != nil {
		return nil, err
	}
	d.compatibilityPolicyMu.Lock()
	d.cachedCompatibilityPolicy = policy
	d.cachedCompatibilityPolicyAt = time.Now()
	d.compatibilityPolicyMu.Unlock()
	return policy, nil
}

func parseCompatibilityPolicy(raw []byte) (*compatibilityPolicy, error) {
	var policy compatibilityPolicy
	if err := yaml.UnmarshalStrict(raw, &policy); err != nil {
		return nil, fmt.Errorf("parse compatibility policy: %w", err)
	}
	policy.fingerprint = fmt.Sprintf("%x", sha256.Sum256(raw))
	if len(policy.Clients) == 0 {
		return nil, fmt.Errorf("compatibility policy contains no clients")
	}
	if policy.APIVersion != "azurelustre.csi.azure.com/v1alpha1" ||
		policy.Kind != "AzureLustreCompatibilityPolicy" {
		return nil, fmt.Errorf("unsupported compatibility policy %s %s", policy.APIVersion, policy.Kind)
	}
	if policy.ClientSetRevision <= 0 || policy.CacheTTLSeconds <= 0 {
		return nil, fmt.Errorf("compatibility policy revision and cache TTL must be positive")
	}
	for flavor, clientPolicy := range policy.Clients {
		if desiredClientIdentity(clientPolicy.Desired.Version, clientPolicy.Desired.ShaSuffix) == "" {
			return nil, fmt.Errorf("compatibility policy for %q has an empty desired client", flavor)
		}
		for _, compatible := range clientPolicy.Compatible {
			if desiredClientIdentity(compatible.Version, compatible.ShaSuffix) == "" {
				return nil, fmt.Errorf("compatibility policy for %q has an empty compatible client", flavor)
			}
		}
	}
	for role, images := range map[string]map[string]compatibilityPolicyImage{
		"driver": policy.Images.Driver,
		"loader": policy.Images.Loader,
	} {
		for flavor, imagePolicy := range images {
			if _, ok := policy.Clients[flavor]; !ok {
				return nil, fmt.Errorf("compatibility policy has a %s image policy without clients for %q", role, flavor)
			}
			required := policy.MountAdmission.Enforce && imagePolicyConfigured(imagePolicy)
			if err := normalizeImagePolicy(role+" "+flavor, &imagePolicy, required); err != nil {
				return nil, err
			}
			images[flavor] = imagePolicy
		}
	}
	if policy.MountAdmission.Enforce {
		configuredFlavors := 0
		for flavor := range policy.Clients {
			driverConfigured := imagePolicyConfigured(policy.Images.Driver[flavor])
			loaderConfigured := imagePolicyConfigured(policy.Images.Loader[flavor])
			if driverConfigured != loaderConfigured {
				return nil, fmt.Errorf("compatibility policy requires both driver and loader image policies for %q", flavor)
			}
			if driverConfigured {
				configuredFlavors++
			}
		}
		// Empty pairs deliberately deny their flavor. Never fill an unused OS
		// with another flavor's digest just to enable admission elsewhere.
		if configuredFlavors == 0 {
			return nil, fmt.Errorf("enforced compatibility policy requires at least one configured image flavor")
		}
	}
	if policy.Bootstrap.Enabled {
		expiresAt, err := time.Parse(time.RFC3339, policy.Bootstrap.ExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("compatibility policy bootstrap expiration is invalid: %w", err)
		}
		if policy.Bootstrap.CSIImage.Repository == "" || policy.Bootstrap.CSIImage.Tag == "" {
			return nil, fmt.Errorf("compatibility policy bootstrap image must be fully specified")
		}
		policy.Bootstrap.CSIImage.Digest = imageDigest(policy.Bootstrap.CSIImage.Digest)
		if policy.Bootstrap.CSIImage.Digest == "" {
			return nil, fmt.Errorf("compatibility policy bootstrap image digest is invalid")
		}
		if expiresAt.IsZero() {
			return nil, fmt.Errorf("compatibility policy bootstrap expiration must be finite")
		}
	}
	return &policy, nil
}

func imagePolicyConfigured(policy compatibilityPolicyImage) bool {
	return policy.DesiredDigest != "" || len(policy.ApprovedDigests) != 0
}

func normalizeImagePolicy(name string, policy *compatibilityPolicyImage, required bool) error {
	if policy.DesiredDigest != "" {
		desired := policy.DesiredDigest
		policy.DesiredDigest = imageDigest(policy.DesiredDigest)
		if policy.DesiredDigest == "" || !strings.EqualFold(desired, policy.DesiredDigest) {
			return fmt.Errorf("compatibility policy %s desired image digest is invalid", name)
		}
	}
	for i, digest := range policy.ApprovedDigests {
		policy.ApprovedDigests[i] = imageDigest(digest)
		if policy.ApprovedDigests[i] == "" || !strings.EqualFold(digest, policy.ApprovedDigests[i]) {
			return fmt.Errorf("compatibility policy %s approved image digest is invalid", name)
		}
	}
	if !required {
		return nil
	}
	if policy.DesiredDigest == "" || !stringInSlice(policy.DesiredDigest, policy.ApprovedDigests) {
		return fmt.Errorf("compatibility policy %s desired digest must be approved", name)
	}
	return nil
}

func (d *Driver) currentCachedCompatibilityPolicy(now time.Time) *compatibilityPolicy {
	d.compatibilityPolicyMu.RLock()
	defer d.compatibilityPolicyMu.RUnlock()
	if d.cachedCompatibilityPolicy == nil {
		return nil
	}
	ttl := time.Duration(d.cachedCompatibilityPolicy.CacheTTLSeconds) * time.Second
	if ttl <= 0 || now.Sub(d.cachedCompatibilityPolicyAt) > ttl {
		return nil
	}
	return d.cachedCompatibilityPolicy
}

func newestFactsByNode(leases []coordinationv1.Lease) map[string]*coordinationv1.Lease {
	result := make(map[string]*coordinationv1.Lease)
	for i := range leases {
		lease := &leases[i]
		nodeName := lease.Annotations[nodeFactAnnotationPrefix+"node-name"]
		if nodeName == "" {
			continue
		}
		current := result[nodeName]
		if current == nil || renewTime(lease).After(renewTime(current)) {
			result[nodeName] = lease
		}
	}
	return result
}

func (d *Driver) computeNodeStatus(
	node *corev1.Node,
	pod *corev1.Pod,
	lease *coordinationv1.Lease,
	policy *compatibilityPolicy,
	policyErr error,
	desiredRevisions map[string]string,
) computedNodeStatus {
	status := d.computeNodeHealth(node, pod, lease, policy, policyErr, desiredRevisions)
	// Quiescence denies new mounts without hiding the evidence needed to decide
	// whether a drained node can safely return to service.
	if strings.EqualFold(strings.TrimSpace(node.Annotations[mountAdmissionAnnotation]), "denied") {
		status.mountAdmission = "Denied"
		status.reason = "AdministrativeQuiescence"
		status.message = fmt.Sprintf("Node annotation %s=denied blocks new mounts.", mountAdmissionAnnotation)
	}
	return status
}

func (d *Driver) computeNodeHealth(
	node *corev1.Node,
	pod *corev1.Pod,
	lease *coordinationv1.Lease,
	policy *compatibilityPolicy,
	policyErr error,
	desiredRevisions map[string]string,
) computedNodeStatus {
	status := computedNodeStatus{
		nodeName:            node.Name,
		nodeUID:             string(node.UID),
		schedulingCoverage:  "Scheduled",
		reporterFreshness:   "Missing",
		csiDelivery:         "Unknown",
		clientCompatibility: "ClientUnknown",
		kernelCoverage:      "Unknown",
		securityCompliance:  "Unknown",
		mountAdmission:      "Denied",
		reason:              "ReporterMissing",
		message:             "The Azure Lustre node pod has not published current node facts.",
		desired:             map[string]interface{}{},
		observed:            map[string]interface{}{},
		activation:          map[string]interface{}{},
	}
	if policy != nil {
		status.desired["policyFingerprint"] = policy.fingerprint
		status.desired["clientSetRevision"] = policy.ClientSetRevision
	}
	if policy != nil && !policy.MountAdmission.Enforce {
		status.mountAdmission = "Allowed"
		status.reason = "AdmissionNotEnforced"
		status.message = "Mount admission enforcement is disabled by policy."
	}
	if policyErr != nil {
		status.reason = "PolicyUnavailable"
		status.message = policyErr.Error()
	}
	if lease == nil {
		if policy != nil && policyErr == nil && legacyBootstrapAllowed(pod, policy.Bootstrap, time.Now()) {
			status.clientCompatibility = "LegacyBootstrapCompatible"
			status.csiDelivery = csiDeliveryState(
				pod,
				pod.Labels["controller-revision-hash"],
				desiredRevisions,
			)
			status.mountAdmission = "Allowed"
			status.reason = "LegacyBootstrapAllowed"
			status.message = fmt.Sprintf(
				"Legacy node pod image is temporarily allowlisted until %s.",
				policy.Bootstrap.ExpiresAt,
			)
			status.activation = map[string]interface{}{
				"nodeUID":     string(node.UID),
				"podUID":      string(pod.UID),
				"podRevision": pod.Labels["controller-revision-hash"],
			}
		}
		return status
	}

	status.observed = stringMapToInterfaceMap(lease.Annotations)
	status.observed["factRenewTime"] = renewTime(lease).UTC().Format(time.RFC3339Nano)
	if !factMatchesPodAndNode(lease, pod, node) || !factIsFresh(lease, time.Now()) {
		status.reporterFreshness = "Stale"
		status.reason = "ReporterStale"
		status.message = "The node fact heartbeat or its Node/Pod identity is stale."
		return status
	}
	status.reporterFreshness = "Current"
	status.activation = map[string]interface{}{
		"nodeUID":     lease.Annotations[nodeFactAnnotationPrefix+"node-uid"],
		"bootID":      lease.Annotations[nodeFactAnnotationPrefix+"boot-id"],
		"podUID":      lease.Annotations[nodeFactAnnotationPrefix+"pod-uid"],
		"podRevision": lease.Annotations[nodeFactAnnotationPrefix+"pod-revision"],
	}
	status.csiDelivery = csiDeliveryState(
		pod,
		lease.Annotations[nodeFactAnnotationPrefix+"pod-revision"],
		desiredRevisions,
	)

	if policyErr != nil || policy == nil {
		return status
	}
	flavor := pod.Labels["flavor"]
	osPolicy, ok := policy.Clients[flavor]
	if !ok {
		status.reason = "FlavorPolicyMissing"
		status.message = fmt.Sprintf("No compatibility policy exists for node flavor %q.", flavor)
		return status
	}
	desired := desiredClientIdentity(osPolicy.Desired.Version, osPolicy.Desired.ShaSuffix)
	status.desired["flavor"] = flavor
	status.desired["driverImageDigest"] = policy.Images.Driver[flavor].DesiredDigest
	status.desired["loaderImageDigest"] = policy.Images.Loader[flavor].DesiredDigest
	status.desired["client"] = desired
	loaded := lease.Annotations[nodeFactAnnotationPrefix+"loaded-client"]
	if loaded == "" {
		status.clientCompatibility = "ClientUnavailable"
		status.reason = "LoadedClientUnknown"
		status.message = "The node did not report a loaded Lustre client."
		return status
	}
	status.kernelCoverage = "Supported"
	switch {
	case loaded == desired:
		status.clientCompatibility = "Exact"
	case clientIsCompatible(loaded, osPolicy.Compatible):
		status.clientCompatibility = "CompatibleNonCurrent"
	default:
		status.clientCompatibility = "UnsafeResidentMismatch"
		status.reason = "UnsupportedLoadedClient"
		status.message = fmt.Sprintf("Loaded client %q is not allowed by the current policy.", loaded)
		return status
	}

	status.securityCompliance = classifySecurityCompliance(lease, flavor, policy.Images)
	if !policy.MountAdmission.Enforce {
		status.mountAdmission = "Allowed"
		status.reason = "AdmissionNotEnforced"
		status.message = "Mount admission enforcement is disabled by policy."
		return status
	}
	if status.securityCompliance == "Unknown" {
		status.reason = "SecurityEvidenceUnknown"
		status.message = "Immutable image-digest approval could not be established."
		return status
	}
	if status.securityCompliance == "Withdrawn" {
		status.reason = "ImageDigestWithdrawn"
		status.message = "The running driver or loader image digest is not approved."
		return status
	}
	if status.csiDelivery == "Unknown" {
		status.reason = "CSIDeliveryUnknown"
		status.message = "The running node pod revision could not be compared with desired state."
		return status
	}
	status.mountAdmission = "Allowed"
	status.reason = "ReadyForNewMounts"
	status.message = "Node evidence satisfies the current mount-admission policy."
	return status
}

func classifySecurityCompliance(
	lease *coordinationv1.Lease,
	flavor string,
	images compatibilityPolicyImages,
) string {
	driverPolicy, ok := images.Driver[flavor]
	if !ok {
		return "Unknown"
	}
	loaderPolicy, ok := images.Loader[flavor]
	if !ok {
		return "Unknown"
	}
	driverDigest := imageDigest(lease.Annotations[nodeFactAnnotationPrefix+"driver-image-id"])
	loaderDigest := imageDigest(lease.Annotations[nodeFactAnnotationPrefix+"loader-image-id"])
	if driverDigest == "" || loaderDigest == "" ||
		driverPolicy.DesiredDigest == "" || loaderPolicy.DesiredDigest == "" {
		return "Unknown"
	}
	if !stringInSlice(driverDigest, driverPolicy.ApprovedDigests) ||
		!stringInSlice(loaderDigest, loaderPolicy.ApprovedDigests) {
		return "Withdrawn"
	}
	if driverDigest == driverPolicy.DesiredDigest && loaderDigest == loaderPolicy.DesiredDigest {
		return "Approved"
	}
	return "NonCurrentApproved"
}

func imageDigest(imageID string) string {
	index := strings.Index(imageID, "sha256:")
	if index < 0 {
		return ""
	}
	digest := imageID[index:]
	if len(digest) < len("sha256:")+64 {
		return ""
	}
	digest = digest[:len("sha256:")+64]
	for _, character := range digest[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return ""
		}
	}
	return strings.ToLower(digest)
}

func csiDeliveryState(pod *corev1.Pod, podRevision string, desiredRevisions map[string]string) string {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind != "DaemonSet" {
			continue
		}
		desiredRevision := desiredRevisions[owner.Name]
		if desiredRevision == "" {
			return "Unknown"
		}
		if podRevision == desiredRevision {
			return "Current"
		}
		return "NonCurrent"
	}
	return "Unknown"
}

func legacyBootstrapAllowed(
	pod *corev1.Pod,
	bootstrap compatibilityPolicyBootstrap,
	now time.Time,
) bool {
	if !bootstrap.Enabled {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, bootstrap.ExpiresAt)
	if err != nil || !now.Before(expiresAt) {
		return false
	}
	expectedImage := bootstrap.CSIImage.Repository + ":" + bootstrap.CSIImage.Tag
	for _, container := range pod.Spec.Containers {
		if container.Name == "azurelustre" {
			if container.Image != expectedImage {
				return false
			}
			status := findContainerStatus(pod.Status.ContainerStatuses, "azurelustre")
			return imageDigest(status.ImageID) == bootstrap.CSIImage.Digest
		}
	}
	return false
}

func desiredDaemonSetRevisions(
	daemonsets []appsv1.DaemonSet,
	revisions []appsv1.ControllerRevision,
) map[string]string {
	result := make(map[string]string, len(daemonsets))
	for i := range daemonsets {
		daemonset := &daemonsets[i]
		if daemonset.Status.ObservedGeneration < daemonset.Generation {
			continue
		}
		var matching *appsv1.ControllerRevision
		ambiguous := false
		for j := range revisions {
			revision := &revisions[j]
			if !metav1.IsControlledBy(revision, daemonset) {
				continue
			}
			var saved struct {
				Spec struct {
					Template *corev1.PodTemplateSpec `json:"template"`
				} `json:"spec"`
			}
			if err := json.Unmarshal(revision.Data.Raw, &saved); err != nil {
				klog.Warningf("ignoring unreadable ControllerRevision %s: %v", revision.Name, err)
				continue
			}
			if saved.Spec.Template == nil || !apiequality.Semantic.DeepEqual(*saved.Spec.Template, daemonset.Spec.Template) {
				continue
			}
			if matching != nil {
				ambiguous = true
				break
			}
			matching = revision
		}
		if matching != nil && !ambiguous {
			result[daemonset.Name] = matching.Labels[appsv1.DefaultDaemonSetUniqueLabelKey]
		}
	}
	return result
}

func staleFactStatus(node *corev1.Node, lease *coordinationv1.Lease) computedNodeStatus {
	return computedNodeStatus{
		nodeName:            node.Name,
		nodeUID:             string(node.UID),
		schedulingCoverage:  "ReporterStale",
		reporterFreshness:   "Stale",
		csiDelivery:         "Unknown",
		clientCompatibility: "ClientUnknown",
		kernelCoverage:      "Unknown",
		securityCompliance:  "Unknown",
		mountAdmission:      "Denied",
		reason:              "NodePodMissing",
		message:             "A node fact exists, but its Azure Lustre node pod is missing.",
		observed:            stringMapToInterfaceMap(lease.Annotations),
		desired:             map[string]interface{}{},
	}
}

func missingNodePodStatus(
	node *corev1.Node,
	daemonset *appsv1.DaemonSet,
	policy *compatibilityPolicy,
	policyErr error,
) computedNodeStatus {
	status := computedNodeStatus{
		nodeName:            node.Name,
		nodeUID:             string(node.UID),
		schedulingCoverage:  "NoEligibleNodePod",
		reporterFreshness:   "Missing",
		csiDelivery:         "Unknown",
		clientCompatibility: "ClientUnknown",
		kernelCoverage:      "Unknown",
		securityCompliance:  "Unknown",
		mountAdmission:      "Denied",
		reason:              "NodePodMissing",
		message:             fmt.Sprintf("DaemonSet %q targets this node, but no node pod exists.", daemonset.Name),
		observed:            map[string]interface{}{},
		desired:             map[string]interface{}{},
	}
	if policyErr != nil {
		status.reason = "PolicyUnavailable"
		status.message = policyErr.Error()
		return status
	}
	if policy == nil {
		return status
	}
	flavor := daemonset.Labels["flavor"]
	if clientPolicy, ok := policy.Clients[flavor]; ok {
		status.desired = map[string]interface{}{
			"clientSetRevision": policy.ClientSetRevision,
			"client": desiredClientIdentity(
				clientPolicy.Desired.Version,
				clientPolicy.Desired.ShaSuffix,
			),
		}
	}
	return status
}

func matchingNodeDaemonSet(node *corev1.Node, daemonsets []appsv1.DaemonSet) *appsv1.DaemonSet {
	for i := range daemonsets {
		daemonset := &daemonsets[i]
		if nodeMatchesPodSpec(node, &daemonset.Spec.Template.Spec) {
			return daemonset
		}
	}
	return nil
}

func nodeMatchesPodSpec(node *corev1.Node, podSpec *corev1.PodSpec) bool {
	for key, value := range podSpec.NodeSelector {
		if node.Labels[key] != value {
			return false
		}
	}
	if affinity := podSpec.Affinity; affinity != nil && affinity.NodeAffinity != nil {
		required := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if required != nil {
			matched := false
			for _, term := range required.NodeSelectorTerms {
				if nodeMatchesSelectorTerm(node, term) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	for _, taint := range node.Spec.Taints {
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		tolerated := false
		for _, toleration := range podSpec.Tolerations {
			if tolerationToleratesTaint(toleration, taint) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			return false
		}
	}
	return true
}

func nodeMatchesSelectorTerm(node *corev1.Node, term corev1.NodeSelectorTerm) bool {
	for _, requirement := range term.MatchExpressions {
		if !selectorRequirementMatches(node.Labels[requirement.Key], node.Labels, requirement) {
			return false
		}
	}
	fields := map[string]string{"metadata.name": node.Name}
	for _, requirement := range term.MatchFields {
		if !selectorRequirementMatches(fields[requirement.Key], fields, requirement) {
			return false
		}
	}
	return true
}

func selectorRequirementMatches(value string, values map[string]string, requirement corev1.NodeSelectorRequirement) bool {
	_, exists := values[requirement.Key]
	switch requirement.Operator {
	case corev1.NodeSelectorOpIn:
		return exists && stringInSlice(value, requirement.Values)
	case corev1.NodeSelectorOpNotIn:
		return !exists || !stringInSlice(value, requirement.Values)
	case corev1.NodeSelectorOpExists:
		return exists
	case corev1.NodeSelectorOpDoesNotExist:
		return !exists
	case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
		if !exists || len(requirement.Values) != 1 {
			return false
		}
		nodeValue, nodeErr := strconv.ParseInt(value, 10, 64)
		requiredValue, requiredErr := strconv.ParseInt(requirement.Values[0], 10, 64)
		if nodeErr != nil || requiredErr != nil {
			return false
		}
		if requirement.Operator == corev1.NodeSelectorOpGt {
			return nodeValue > requiredValue
		}
		return nodeValue < requiredValue
	default:
		return false
	}
}

func tolerationToleratesTaint(toleration corev1.Toleration, taint corev1.Taint) bool {
	if toleration.Effect != "" && toleration.Effect != taint.Effect {
		return false
	}
	if toleration.Key == "" && toleration.Operator == corev1.TolerationOpExists {
		return true
	}
	if toleration.Key != taint.Key {
		return false
	}
	return toleration.Operator == corev1.TolerationOpExists ||
		(toleration.Operator == corev1.TolerationOpEqual && toleration.Value == taint.Value)
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func (d *Driver) writeNodeStatus(ctx context.Context, status computedNodeStatus, snapshot *unstructured.Unstructured) error {
	refresh := false
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if refresh {
			var err error
			snapshot, err = d.dynamicClient.Resource(nodeStatusGVR).Namespace(d.podNamespace).Get(ctx, status.nodeName, metav1.GetOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if apierrors.IsNotFound(err) {
				snapshot = nil
			}
		}
		err := d.writeNodeStatusOnce(ctx, status, snapshot)
		refresh = apierrors.IsConflict(err)
		return err
	})
}

func (d *Driver) writeNodeStatusOnce(ctx context.Context, status computedNodeStatus, snapshot *unstructured.Unstructured) error {
	resource := d.dynamicClient.Resource(nodeStatusGVR).Namespace(d.podNamespace)
	object := snapshot.DeepCopy()
	var err error
	if object == nil {
		object, err = resource.Create(ctx, &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "azurelustre.csi.azure.com/v1alpha1",
			"kind":       "AzureLustreNodeStatus",
			"metadata": map[string]interface{}{
				"name":      status.nodeName,
				"namespace": d.podNamespace,
			},
			"spec": map[string]interface{}{
				"nodeName": status.nodeName,
				"nodeUID":  status.nodeUID,
			},
		}}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			object, err = resource.Get(ctx, status.nodeName, metav1.GetOptions{})
		}
	}
	if err != nil {
		return fmt.Errorf("get or create node status %q: %w", status.nodeName, err)
	}
	currentNodeUID, _, err := unstructured.NestedString(object.Object, "spec", "nodeUID")
	if err != nil {
		return fmt.Errorf("read node status spec %q: %w", status.nodeName, err)
	}
	currentNodeName, _, err := unstructured.NestedString(object.Object, "spec", "nodeName")
	if err != nil {
		return fmt.Errorf("read node status spec %q: %w", status.nodeName, err)
	}
	if currentNodeUID != status.nodeUID || currentNodeName != status.nodeName {
		if err := unstructured.SetNestedMap(object.Object, map[string]interface{}{
			"nodeName": status.nodeName,
			"nodeUID":  status.nodeUID,
		}, "spec"); err != nil {
			return fmt.Errorf("build node status spec %q: %w", status.nodeName, err)
		}
		object, err = resource.Update(ctx, object, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("update node status spec %q: %w", status.nodeName, err)
		}
	}
	condition := mountAdmissionCondition(object, status, time.Now().UTC())
	if err := unstructured.SetNestedMap(object.Object, map[string]interface{}{
		"observedAt":          time.Now().UTC().Format(time.RFC3339),
		"schedulingCoverage":  status.schedulingCoverage,
		"reporterFreshness":   status.reporterFreshness,
		"csiDelivery":         status.csiDelivery,
		"clientCompatibility": status.clientCompatibility,
		"kernelCoverage":      status.kernelCoverage,
		"securityCompliance":  status.securityCompliance,
		"mountAdmission":      status.mountAdmission,
		"reason":              status.reason,
		"message":             status.message,
		"desired":             status.desired,
		"observed":            status.observed,
		"activation":          status.activation,
		"conditions":          []interface{}{condition},
	}, "status"); err != nil {
		return fmt.Errorf("build node status %q: %w", status.nodeName, err)
	}
	if _, err := resource.UpdateStatus(ctx, object, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update node status %q: %w", status.nodeName, err)
	}
	return nil
}

func mountAdmissionCondition(
	object *unstructured.Unstructured,
	status computedNodeStatus,
	now time.Time,
) map[string]interface{} {
	conditionStatus := "False"
	if status.mountAdmission == "Allowed" {
		conditionStatus = "True"
	}
	transitionTime := now.Format(time.RFC3339)
	conditions, found, err := unstructured.NestedSlice(object.Object, "status", "conditions")
	if err == nil && found {
		for _, item := range conditions {
			condition, ok := item.(map[string]interface{})
			if !ok || condition["type"] != "ReadyForNewMounts" {
				continue
			}
			if condition["status"] == conditionStatus && condition["reason"] == status.reason {
				if previous, ok := condition["lastTransitionTime"].(string); ok {
					transitionTime = previous
				}
			}
			break
		}
	}
	return map[string]interface{}{
		"type":               "ReadyForNewMounts",
		"status":             conditionStatus,
		"reason":             status.reason,
		"message":            status.message,
		"lastTransitionTime": transitionTime,
	}
}

func (d *Driver) garbageCollectNodeStatus(
	ctx context.Context,
	status *unstructured.Unstructured,
	nodesByName map[string]*corev1.Node,
) error {
	resource := d.dynamicClient.Resource(nodeStatusGVR).Namespace(d.podNamespace)
	nodeName, _, err := unstructured.NestedString(status.Object, "spec", "nodeName")
	if err != nil {
		return fmt.Errorf("read node name from status %q: %w", status.GetName(), err)
	}
	nodeUID, _, err := unstructured.NestedString(status.Object, "spec", "nodeUID")
	if err != nil {
		return fmt.Errorf("read node UID from status %q: %w", status.GetName(), err)
	}
	node := nodesByName[nodeName]
	if status.GetName() == nodeName && node != nil && string(node.UID) == nodeUID {
		return nil
	}
	deleteOptions := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{
		UID: ptr(status.GetUID()), ResourceVersion: ptr(status.GetResourceVersion()),
	}}
	if err := resource.Delete(ctx, status.GetName(), deleteOptions); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete stale node status %q: %w", status.GetName(), err)
	}
	return nil
}

func factMatchesPodAndNode(lease *coordinationv1.Lease, pod *corev1.Pod, node *corev1.Node) bool {
	annotations := lease.Annotations
	if node.UID == "" || pod.UID == "" || pod.DeletionTimestamp != nil || node.DeletionTimestamp != nil {
		return false
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != string(pod.UID) {
		return false
	}
	if annotations[nodeFactAnnotationPrefix+"schema-version"] != nodeFactSchemaVersion ||
		pod.Labels["flavor"] == "" ||
		annotations[nodeFactAnnotationPrefix+"flavor"] != pod.Labels["flavor"] ||
		annotations[nodeFactAnnotationPrefix+"node-name"] != node.Name ||
		annotations[nodeFactAnnotationPrefix+"node-uid"] != string(node.UID) ||
		annotations[nodeFactAnnotationPrefix+"pod-name"] != pod.Name ||
		annotations[nodeFactAnnotationPrefix+"pod-uid"] != string(pod.UID) ||
		annotations[nodeFactAnnotationPrefix+"pod-revision"] != pod.Labels["controller-revision-hash"] {
		return false
	}
	if annotations[nodeFactAnnotationPrefix+"provider-id"] != node.Spec.ProviderID {
		return false
	}
	if node.Status.NodeInfo.BootID != "" &&
		annotations[nodeFactAnnotationPrefix+"boot-id"] != node.Status.NodeInfo.BootID {
		return false
	}
	if node.Status.NodeInfo.KernelVersion != "" &&
		annotations[nodeFactAnnotationPrefix+"kernel-version"] != node.Status.NodeInfo.KernelVersion {
		return false
	}
	driverStatus := findContainerStatus(pod.Status.ContainerStatuses, "azurelustre")
	if driverStatus.ImageID == "" || driverStatus.ContainerID == "" ||
		annotations[nodeFactAnnotationPrefix+"driver-image-id"] != driverStatus.ImageID ||
		annotations[nodeFactAnnotationPrefix+"driver-container-id"] != driverStatus.ContainerID ||
		annotations[nodeFactAnnotationPrefix+"driver-restart-count"] != strconv.Itoa(int(driverStatus.RestartCount)) {
		return false
	}
	loaderStatus := findContainerStatus(pod.Status.InitContainerStatuses, "lustre-loader")
	return loaderStatus.ImageID != "" && loaderStatus.ContainerID != "" &&
		annotations[nodeFactAnnotationPrefix+"loader-image-id"] == loaderStatus.ImageID &&
		annotations[nodeFactAnnotationPrefix+"loader-container-id"] == loaderStatus.ContainerID &&
		annotations[nodeFactAnnotationPrefix+"loader-restart-count"] == strconv.Itoa(int(loaderStatus.RestartCount))
}

func factIsFresh(lease *coordinationv1.Lease, now time.Time) bool {
	if lease.Spec.RenewTime == nil || lease.Spec.LeaseDurationSeconds == nil {
		return false
	}
	if *lease.Spec.LeaseDurationSeconds <= 0 || *lease.Spec.LeaseDurationSeconds > nodeFactLeaseDuration {
		return false
	}
	renewed := lease.Spec.RenewTime.Time
	if renewed.After(now.Add(30 * time.Second)) {
		return false
	}
	return now.Sub(renewed) <= time.Duration(*lease.Spec.LeaseDurationSeconds)*time.Second
}

func renewTime(lease *coordinationv1.Lease) time.Time {
	if lease.Spec.RenewTime == nil {
		return time.Time{}
	}
	return lease.Spec.RenewTime.Time
}

func clientIsCompatible(loaded string, compatible []clientIdentity) bool {
	for _, client := range compatible {
		if loaded == desiredClientIdentity(client.Version, client.ShaSuffix) {
			return true
		}
	}
	return false
}

func stringMapToInterfaceMap(values map[string]string) map[string]interface{} {
	result := make(map[string]interface{}, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
