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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	mount "k8s.io/mount-utils"
)

func testNodeStartup(t *testing.T) *nodeStartup {
	t.Helper()
	return &nodeStartup{
		dir:           t.TempDir(),
		identity:      uuid.New() + " " + uuid.New() + " 2.17.0_test",
		retryDelay:    time.Millisecond,
		probeInterval: time.Millisecond,
		install:       func(context.Context) error { return nil },
		probe:         func(context.Context) error { return nil },
		observe: func() (nodeStartupRuntime, error) {
			return nodeStartupRuntime{loadedClient: "2.17.0_test", policyFingerprint: strings.Repeat("a", 64)}, nil
		},
	}
}

func writeTestRuntimeReady(t *testing.T, s *nodeStartup) *nodeStartupObservation {
	t.Helper()
	runtime, err := s.observeRuntime()
	require.NoError(t, err)
	observation := &nodeStartupObservation{runtime: runtime, observedAt: time.Now()}
	require.NoError(t, s.publishReady(observation))
	s.ready.Store(observation)
	return observation
}

func writeTestLoaderReady(t *testing.T, s *nodeStartup) string {
	t.Helper()
	record := s.identity + " " + uuid.New() + " 1\n"
	for _, name := range []string{"loader-started", "loader-ready"} {
		require.NoError(t, os.WriteFile(filepath.Join(s.dir, name), []byte(record), 0o600))
	}
	return record
}

func TestCleanupCapableStartupRPCRecovery(t *testing.T) {
	driver, mounter, _ := newNodeMountTestDriver(t)
	s := testNodeStartup(t)
	driver.nodeStartup = s
	var attempts atomic.Int32
	recoverInstall := make(chan struct{})
	s.install = func(ctx context.Context) error {
		if attempts.Add(1) == 1 {
			return errors.New("package repository unavailable")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-recoverInstall:
			return nil
		}
	}
	existingTarget := t.TempDir()
	mounter.MountPoints = []mount.MountPoint{{Device: "127.0.0.1@tcp:/lustrefs", Path: existingTarget, Type: "lustre"}}
	ctx, cancel := context.WithCancel(t.Context())
	stopStartup := driver.startNodeStartup(ctx)
	socketPath := filepath.Join(t.TempDir(), "csi.sock")
	finished := make(chan error, 1)
	go func() { finished <- RunGRPCServer(ctx, "unix://"+socketPath, driver, nil, driver) }()
	t.Cleanup(func() {
		cancel()
		stopStartup()
		require.NoError(t, <-finished)
		assert.NoFileExists(t, filepath.Join(s.dir, "driver-ready"))
	})
	connection := newLifecycleClient(t, "unix://"+socketPath)
	rpcCtx, stopRPC := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopRPC()
	probe, err := csi.NewIdentityClient(connection).Probe(rpcCtx, &csi.ProbeRequest{}, grpc.WaitForReady(true))
	require.NoError(t, err)
	assert.True(t, probe.GetReady().GetValue(), "package failure must not make CSI liveness fail")
	before, err := os.Stat(socketPath)
	require.NoError(t, err)
	client := csi.NewNodeClient(connection)
	request := &csi.NodePublishVolumeRequest{
		VolumeId:      "volume#lustrefs#127.0.0.1##f#",
		TargetPath:    existingTarget,
		VolumeContext: map[string]string{VolumeContextMGSIPAddress: "127.0.0.1"},
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
		},
	}
	_, err = client.NodePublishVolume(rpcCtx, request)
	require.NoError(t, err, "already-published retries must bypass the startup gate")
	assert.Empty(t, mounter.GetLog())
	_, err = client.NodeUnpublishVolume(rpcCtx, &csi.NodeUnpublishVolumeRequest{VolumeId: request.GetVolumeId(), TargetPath: existingTarget})
	require.NoError(t, err, "ordinary unpublish must work while installation fails")
	assert.Empty(t, mounter.MountPoints)
	mounter.ResetLog()
	request.TargetPath = filepath.Join(t.TempDir(), "new")
	_, err = client.NodePublishVolume(rpcCtx, request)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Empty(t, mounter.GetLog(), "default-off compatibility policy must not bypass startup readiness")
	require.Eventually(t, func() bool { return attempts.Load() >= 2 }, time.Second, time.Millisecond)
	close(recoverInstall)
	require.Eventually(t, s.isReady, time.Second, time.Millisecond,
		"healthy host runtime must permit mounts without loader completion records")
	_, err = client.NodePublishVolume(rpcCtx, request)
	require.NoError(t, err)
	require.Len(t, mounter.MountPoints, 1)
	after, err := os.Stat(socketPath)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "recovery must reuse the live CSI endpoint")
	_, err = client.NodeUnpublishVolume(rpcCtx, &csi.NodeUnpublishVolumeRequest{VolumeId: request.GetVolumeId(), TargetPath: request.GetTargetPath()})
	require.NoError(t, err)
	assert.Empty(t, mounter.MountPoints)
	assert.Equal(t, int32(2), attempts.Load(), "successful setup must stop mutating packages")
}

func TestNodeStartupUsesHostRuntimeDuringLoaderRestart(t *testing.T) {
	s := testNodeStartup(t)
	writeTestRuntimeReady(t, s)
	require.True(t, s.isReady(), "host readiness does not require loader records")
	old := writeTestLoaderReady(t, s)
	writeTestLoaderReady(t, s)
	require.NoError(t, os.WriteFile(filepath.Join(s.dir, "loader-ready"), []byte(old), 0o600))
	assert.True(t, s.isReady(), "loader-generation changes must not invalidate healthy host evidence")
	s.ready.Store(nil)
	assert.False(t, s.isReady())
}

func TestNodeStartupRejectsInvalidRuntimeObservations(t *testing.T) {
	for _, scenario := range []string{"expired", "future", "different module", "different policy", "unreadable runtime"} {
		t.Run(scenario, func(t *testing.T) {
			s := testNodeStartup(t)
			observation := writeTestRuntimeReady(t, s)
			current := observation.runtime
			switch scenario {
			case "expired":
				observation.observedAt = time.Now().Add(-nodeStartupMaxAge - time.Second)
			case "future":
				observation.observedAt = time.Now().Add(time.Second)
			case "different module":
				current.loadedClient = "2.18.0_other"
			case "different policy":
				current.policyFingerprint = strings.Repeat("b", 64)
			}
			s.observe = func() (nodeStartupRuntime, error) {
				if scenario == "unreadable runtime" {
					return nodeStartupRuntime{}, errors.New("runtime unavailable")
				}
				return current, nil
			}
			assert.False(t, s.isReady())
		})
	}
}

func TestObserveNodeStartupRuntimeCompatibility(t *testing.T) {
	for _, test := range []struct {
		name, loaded, flavor string
		missingPolicy        bool
		allowed              bool
	}{
		{name: "exact", loaded: "2.17.0_current", flavor: "jammy", allowed: true},
		{name: "supported skew", loaded: "2.18.0_newer", flavor: "jammy", allowed: true},
		{name: "unsupported client", loaded: "2.16.0_other", flavor: "jammy"},
		{name: "no resident client", flavor: "jammy"},
		{name: "unknown flavor", loaded: "2.17.0_current", flavor: "unknown"},
		{name: "missing policy", loaded: "2.17.0_current", flavor: "jammy", missingPolicy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver := &Driver{
				nodeFlavor: test.flavor,
				nodeFactReadFile: func(string) ([]byte, error) {
					return []byte(test.loaded), nil
				},
				mountAdmissionPolicyPath: "policy",
				mountAdmissionReadFile: func(string) ([]byte, error) {
					if test.missingPolicy {
						return nil, os.ErrNotExist
					}
					return []byte(testCompatibilityPolicy), nil
				},
			}
			observation, err := driver.observeNodeStartupRuntime()
			if test.allowed {
				require.NoError(t, err)
				assert.Equal(t, test.loaded, observation.loadedClient)
				assert.NotEmpty(t, observation.policyFingerprint)
			} else {
				require.Error(t, err, "unknown compatibility must deny even when optional image enforcement is off")
			}
		})
	}
}

func TestNodeStartupRejectsRuntimeChangeDuringProbe(t *testing.T) {
	s := testNodeStartup(t)
	var observations atomic.Int32
	s.observe = func() (nodeStartupRuntime, error) {
		loaded := "before"
		if observations.Add(1)%2 == 0 {
			loaded = "after"
		}
		return nodeStartupRuntime{loadedClient: loaded, policyFingerprint: "policy"}, nil
	}
	driver := &Driver{nodeStartup: s}
	stop := driver.startNodeStartup(t.Context())
	defer stop()
	require.Eventually(t, func() bool { return observations.Load() >= 4 }, time.Second, time.Millisecond)
	assert.False(t, s.isReady())
	assert.NoFileExists(t, filepath.Join(s.dir, "driver-ready"))
}

func TestNodeStartupRetainsTaintUntilRuntimeRecovery(t *testing.T) {
	s := testNodeStartup(t)
	writeTestLoaderReady(t, s)
	var healthy atomic.Bool
	var probed atomic.Bool
	s.probe = func(context.Context) error {
		probed.Store(true)
		if !healthy.Load() {
			return errors.New("runtime unavailable")
		}
		return nil
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{{
		Key: DefaultDriverName + AgentNotReadyNodeTaintKeySuffix, Effect: corev1.TaintEffectNoSchedule,
	}}}}
	client := fake.NewSimpleClientset(node)
	driver := &Driver{
		CSIDriver:           CSIDriver{Name: DefaultDriverName, NodeID: node.Name},
		nodeStartup:         s,
		kubeClient:          client,
		removeNotReadyTaint: true,
	}
	stop := driver.startNodeStartup(t.Context())
	defer stop()
	require.Eventually(t, probed.Load, time.Second, time.Millisecond)
	assert.False(t, s.isReady())
	assert.NoFileExists(t, filepath.Join(s.dir, "driver-ready"), "installed tools alone must not advance Pod readiness")
	current, err := client.CoreV1().Nodes().Get(t.Context(), node.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Len(t, current.Spec.Taints, 1, "installed tools alone must not remove the taint")
	healthy.Store(true)
	require.Eventually(t, func() bool {
		current, err := client.CoreV1().Nodes().Get(t.Context(), node.Name, metav1.GetOptions{})
		return err == nil && len(current.Spec.Taints) == 0
	}, time.Second, time.Millisecond)
	healthy.Store(false)
	require.Eventually(t, func() bool { return !s.isReady() }, time.Second, time.Millisecond)
}

func TestNodeStartupStopsInstallerOnCancellation(t *testing.T) {
	s := testNodeStartup(t)
	entered := make(chan struct{})
	stopped := make(chan struct{})
	s.install = func(ctx context.Context) error {
		close(entered)
		defer close(stopped)
		<-ctx.Done()
		return ctx.Err()
	}
	driver := &Driver{nodeStartup: s}
	stop := driver.startNodeStartup(t.Context())
	<-entered
	stop()
	select {
	case <-stopped:
	default:
		t.Fatal("startup shutdown returned before its installer stopped")
	}
	assert.False(t, s.isReady())
	assert.NoFileExists(t, filepath.Join(s.dir, "driver-ready"))
}

func TestStartupCommandCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := runStartupCommand(exec.CommandContext(ctx, "sleep", "30"))
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "owned command must stop on cancellation")
}

func TestNewNodeStartupClearsStalePackages(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POD_UID", uuid.New())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "driver-ready"), []byte("old"), 0o600))
	s, err := newNodeStartup(dir, "2.17.0_test")
	require.NoError(t, err)
	assert.False(t, s.isReady())
	assert.NoFileExists(t, filepath.Join(dir, "driver-ready"))
	t.Setenv("POD_UID", "")
	_, err = newNodeStartup(dir, "2.17.0_test")
	require.Error(t, err)
}
