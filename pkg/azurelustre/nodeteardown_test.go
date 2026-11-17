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
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/pborman/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	mount "k8s.io/mount-utils"
)

func testNodeTeardown(t *testing.T) *nodeTeardown {
	t.Helper()
	return &nodeTeardown{
		dir:      t.TempDir(),
		identity: "11111111-1111-4111-8111-111111111111 22222222-2222-4222-8222-222222222222",
		podsDir:  t.TempDir(),
		timeout:  30 * time.Millisecond,
		interval: time.Millisecond,
		modulesGone: func() error {
			return errors.New("fake modules are still loaded")
		},
		unload: func(context.Context) error {
			return errors.New("fake modules are busy")
		},
	}
}

func testTeardownRequest(t *testing.T, g *nodeTeardown) string {
	t.Helper()
	request := g.identity + " " + uuid.New()
	require.NoError(t, g.write("request", request))
	return request
}

func TestNewDriverRejectsNonNodeTeardown(t *testing.T) {
	for _, test := range []struct {
		name                 string
		nodeID               string
		statusControllerOnly bool
	}{
		{"controller", "", false},
		{"status controller", "", true},
		{"status controller with node ID", "test-node", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewDriver(t.Context(), &DriverOptions{
				NodeID:                      test.nodeID,
				StatusControllerOnly:        test.statusControllerOnly,
				ExperimentalNodeTeardownDir: t.TempDir(),
			})
			require.ErrorContains(t, err, "experimental node teardown is only supported by the node driver")
		})
	}
}

func TestNodeTeardownAcknowledgement(t *testing.T) {
	tests := []struct {
		name        string
		record      string
		wrongNonce  bool
		probeErr    error
		wantSuccess bool
	}{
		{name: "matching completion and absent modules", record: "completed", wantSuccess: true},
		{name: "no acknowledgement despite absent modules"},
		{name: "wrong acknowledgement", record: "completed", wrongNonce: true},
		{name: "acknowledgement with loaded modules", record: "completed", probeErr: errors.New("lnet still loaded")},
		{name: "acknowledgement with unreadable modules", record: "completed", probeErr: os.ErrPermission},
		{name: "failed worker", record: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testNodeTeardown(t)
			request := testTeardownRequest(t, g)
			g.modulesGone = func() error { return tt.probeErr }
			if tt.record != "" {
				record := request
				if tt.wrongNonce {
					record = g.identity + " " + uuid.New()
				}
				require.NoError(t, g.write(tt.record, record))
			}
			err := g.waitForCompletion(t.Context(), request)
			if tt.wantSuccess {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestNodeTeardownInvalidRequests(t *testing.T) {
	for _, record := range []string{
		"", "\n", "invalid",
		"33333333-3333-4333-8333-333333333333 22222222-2222-4222-8222-222222222222 " + uuid.New(),
		"11111111-1111-4111-8111-111111111111 33333333-3333-4333-8333-333333333333 " + uuid.New(),
	} {
		t.Run(record, func(t *testing.T) {
			g := testNodeTeardown(t)
			require.NoError(t, os.WriteFile(filepath.Join(g.dir, "request"), []byte(record), 0o600))
			calls := 0
			g.unload = func(context.Context) error { calls++; return nil }
			require.Error(t, g.runWorker(t.Context()))
			require.Error(t, g.admitPublish())
			require.Zero(t, calls)
		})
	}
}

func TestNodeTeardownWorker(t *testing.T) {
	tests := []struct {
		name        string
		busy        bool
		probeErr    error
		wantSuccess bool
	}{
		{name: "ordinary unload retries then completes", wantSuccess: true},
		{name: "unload remains busy", busy: true},
		{name: "helper succeeds but modules remain", probeErr: errors.New("mdc still loaded")},
		{name: "module probe fails", probeErr: os.ErrPermission},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := testNodeTeardown(t)
			request := testTeardownRequest(t, g)
			calls := 0
			g.unload = func(context.Context) error {
				calls++
				if tt.busy || calls < 2 {
					return errors.New("busy")
				}
				return nil
			}
			g.modulesGone = func() error { return tt.probeErr }
			err := g.runWorker(t.Context())
			completed, readErr := g.read("completed")
			require.NoError(t, readErr)
			if tt.wantSuccess {
				require.NoError(t, err)
				require.Equal(t, request, completed)
				require.Equal(t, 2, calls)
			} else {
				require.Error(t, err)
				require.Empty(t, completed)
			}
			beforeRestart := calls
			restarted := testNodeTeardown(t)
			restarted.dir, restarted.unload, restarted.modulesGone = g.dir, g.unload, g.modulesGone
			err = restarted.runWorker(t.Context())
			require.Equal(t, beforeRestart, calls, "restart must not retry a completed or failed request")
			if tt.wantSuccess {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestNodeTeardownHeldUntilMatchingRelease(t *testing.T) {
	g := testNodeTeardown(t)
	request := testTeardownRequest(t, g)
	require.NoError(t, g.write("test-hold", g.identity))
	require.NoError(t, g.write("test-release", g.identity+" "+uuid.New()))
	var calls atomic.Int32
	g.unload = func(context.Context) error { calls.Add(1); return nil }
	g.modulesGone = func() error { return nil }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- g.runWorker(ctx) }()
	require.Eventually(t, func() bool {
		quiesced, err := g.read("quiesced")
		return err == nil && quiesced == request
	}, time.Second, time.Millisecond)
	require.Error(t, g.waitForCompletion(ctx, request))
	require.Zero(t, calls.Load(), "withheld release must prevent any module operation")
	require.NoError(t, g.write("test-release", request))
	require.NoError(t, <-done)
	require.EqualValues(t, 1, calls.Load())
	require.NoError(t, g.waitForCompletion(ctx, request))
}

func TestNodeTeardownWorkerCancellation(t *testing.T) {
	g := testNodeTeardown(t)
	request := testTeardownRequest(t, g)
	require.NoError(t, g.write("test-hold", g.identity))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, g.runWorker(ctx), context.Canceled)
	require.Error(t, g.waitForCompletion(t.Context(), request))
	completed, err := g.read("completed")
	require.NoError(t, err)
	require.Empty(t, completed)
}

func TestVerifyLustreModulesGone(t *testing.T) {
	for _, source := range []string{"sys", "proc"} {
		for _, module := range lustreClientModules {
			t.Run(source+"/"+module, func(t *testing.T) {
				dir := t.TempDir()
				proc := filepath.Join(t.TempDir(), "modules")
				require.NoError(t, os.WriteFile(proc, nil, 0o600))
				if source == "sys" {
					require.NoError(t, os.Mkdir(filepath.Join(dir, module), 0o700))
				} else {
					require.NoError(t, os.WriteFile(proc, []byte(module+" 4096 1 - Live 0x0\n"), 0o600))
				}
				require.ErrorContains(t, verifyLustreModulesGone(dir, proc), module)
			})
		}
	}
	dir := t.TempDir()
	proc := filepath.Join(t.TempDir(), "modules")
	require.NoError(t, os.WriteFile(proc, []byte("unrelated 4096 0 - Live 0x0\n"), 0o600))
	require.NoError(t, verifyLustreModulesGone(dir, proc))
	require.Error(t, verifyLustreModulesGone(filepath.Join(dir, "missing"), proc))
	require.Error(t, verifyLustreModulesGone(dir, filepath.Join(dir, "missing")))
}

func testTeardownDriver(t *testing.T, cordoned bool) (*Driver, *mount.FakeMounter) {
	t.Helper()
	d := NewFakeDriver(t)
	d.NodeID = "test-node"
	d.teardown = testNodeTeardown(t)
	d.kubeClient = fake.NewClientset(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: d.NodeID},
		Spec:       v1.NodeSpec{Unschedulable: cordoned},
	})
	mounter := mount.NewFakeMounter(nil)
	d.mounter = &mount.SafeFormatAndMount{Interface: mounter}
	d.workingMountDir = t.TempDir()
	// Deliberately leave forceMounter nil. This opt-in path must never use it.
	return d, mounter
}

func TestNodeTeardownLastUnpublishAndRetry(t *testing.T) {
	d, mounter := testTeardownDriver(t, true)
	first := filepath.Join(d.teardown.podsDir, uuid.New(), "volumes/kubernetes.io~csi/pv/mount")
	last := filepath.Join(d.teardown.podsDir, uuid.New(), "volumes/kubernetes.io~csi/pv/mount")
	for _, target := range []string{first, last} {
		require.NoError(t, os.MkdirAll(target, 0o700))
		require.NoError(t, mounter.Mount("fake:/fs", target, "lustre", nil))
	}
	_, err := d.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: "first", TargetPath: first})
	require.NoError(t, err, "earlier Pod deletion must not wait for other Pod unmounts")
	request, err := d.teardown.request()
	require.NoError(t, err)
	require.Empty(t, request)

	req := &csi.NodeUnpublishVolumeRequest{VolumeId: "last", TargetPath: last}
	_, err = d.NodeUnpublishVolume(t.Context(), req)
	require.Error(t, err, "unmount alone must not release the last Pod")
	require.NoDirExists(t, last)
	request, err = d.teardown.request()
	require.NoError(t, err)
	require.NotEmpty(t, request)
	require.True(t, d.teardown.mu.TryLock(), "completion wait must release lifecycle lock")
	d.teardown.mu.Unlock()
	require.True(t, d.kernelModuleLock.TryLock(), "completion wait must release module lock")
	d.kernelModuleLock.Unlock()

	restarted := testNodeTeardown(t)
	restarted.dir = d.teardown.dir
	d.teardown = restarted
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = d.NodeUnpublishVolume(ctx, req)
	require.Error(t, err, "missing target and driver restart must not bypass pending cleanup")
	afterRestart, err := d.teardown.request()
	require.NoError(t, err)
	require.Equal(t, request, afterRestart)
	require.NoError(t, d.teardown.write("completed", request))
	d.teardown.modulesGone = func() error { return nil }
	_, err = d.NodeUnpublishVolume(t.Context(), req)
	require.NoError(t, err)
}

func TestNodeTeardownRetriesTemporaryCleanupBeforeRequest(t *testing.T) {
	d, mounter := testTeardownDriver(t, true)
	target := filepath.Join(d.teardown.podsDir, uuid.New(), "volumes/kubernetes.io~csi/pv/mount")
	internalTarget, err := getInternalMountPath(d.workingMountDir, target)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(internalTarget, 0o700))
	require.NoError(t, mounter.Mount("fake:/fs", internalTarget, "lustre", nil))
	mounter.UnmountFunc = func(string) error { return errors.New("temporary unmount failed") }
	req := &csi.NodeUnpublishVolumeRequest{VolumeId: "last", TargetPath: target}

	_, err = d.NodeUnpublishVolume(t.Context(), req)

	require.ErrorContains(t, err, "temporary mount cleanup incomplete")
	require.Len(t, mounter.MountPoints, 1)
	request, err := d.teardown.request()
	require.NoError(t, err)
	require.Empty(t, request, "failed temporary cleanup must not start module teardown")

	mounter.UnmountFunc = nil
	_, err = d.NodeUnpublishVolume(t.Context(), req)

	require.ErrorContains(t, err, "node teardown is incomplete")
	require.Empty(t, mounter.MountPoints)
	require.NoDirExists(t, internalTarget)
	request, err = d.teardown.request()
	require.NoError(t, err)
	require.NotEmpty(t, request)
}

func TestNodeTeardownOnlyCordonedNodes(t *testing.T) {
	d, _ := testTeardownDriver(t, false)
	require.NoError(t, d.finishNodeTeardown(t.Context()))
	request, err := d.teardown.request()
	require.NoError(t, err)
	require.Empty(t, request)
	d.kubeClient = nil
	require.Error(t, d.finishNodeTeardown(t.Context()), "unknown node state must not permit retirement")
}

func TestNodeTeardownForeignMountBlocksCompletion(t *testing.T) {
	d, mounter := testTeardownDriver(t, true)
	require.NoError(t, mounter.Mount("fake:/fs", "/foreign-mount", "lustre", nil))
	temporary := filepath.Join(t.TempDir(), d.teardown.podsDir, uuid.New(), "volumes/kubernetes.io~csi/pv/mount")
	require.NoError(t, mounter.Mount("fake:/fs", temporary, "lustre", nil))
	require.Error(t, d.finishNodeTeardown(t.Context()))
	request, err := d.teardown.request()
	require.NoError(t, err)
	require.NotEmpty(t, request, "foreign mounts must not make the last CSI unpublish return OK")
	require.NoError(t, d.teardown.write("completed", request))
	require.Error(t, d.finishNodeTeardown(t.Context()), "ack cannot override loaded modules")
}

func TestNodeTeardownRejectsNewPublish(t *testing.T) {
	d, mounter := testTeardownDriver(t, true)
	testTeardownRequest(t, d.teardown)
	target := filepath.Join(t.TempDir(), "mount")
	req := &csi.NodePublishVolumeRequest{
		VolumeId:      "vol#lustrefs#1.1.1.1",
		TargetPath:    target,
		VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs", "sub-dir": "new-directory"},
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		},
	}
	_, err := d.NodePublishVolume(t.Context(), req)
	require.ErrorContains(t, err, "new mounts are disabled")
	require.Empty(t, mounter.GetLog(), "no temporary or final mount may start")
	require.True(t, d.teardown.mu.TryLock())
	d.teardown.mu.Unlock()
}

func TestNodeTeardownPreservesMountAdmission(t *testing.T) {
	for _, test := range []struct {
		name           string
		teardownQueued bool
		alreadyMounted bool
	}{
		{"admission denies before teardown", false, false},
		{"admission denies during teardown", true, false},
		{"mounted retry bypasses both gates", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, mounter := testTeardownDriver(t, true)
			d.mountAdmissionPolicyEnabled = true
			d.mountAdmissionReadFile = func(string) ([]byte, error) {
				return []byte(testEnforcedCompatibilityPolicy), nil
			}
			if test.teardownQueued {
				testTeardownRequest(t, d.teardown)
			}
			target := filepath.Join(t.TempDir(), "mount")
			if test.alreadyMounted {
				require.NoError(t, os.MkdirAll(target, 0o700))
				require.NoError(t, mounter.Mount("1.1.1.1@tcp:/lustrefs", target, "lustre", nil))
			}
			priorActions := len(mounter.GetLog())
			_, err := d.NodePublishVolume(t.Context(), &csi.NodePublishVolumeRequest{
				VolumeId:      "vol#lustrefs#1.1.1.1",
				TargetPath:    target,
				VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
					AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
				},
			})
			if test.alreadyMounted {
				require.NoError(t, err)
			} else {
				require.Equal(t, codes.FailedPrecondition, status.Code(err))
				require.ErrorContains(t, err, "StatusUnavailable")
			}
			require.Len(t, mounter.GetLog(), priorActions, "neither gate may start a mount")
		})
	}
}

type teardownBlockingMounter struct {
	*mount.FakeMounter
	entered chan struct{}
	release <-chan struct{}
}

func (m *teardownBlockingMounter) MountSensitiveWithoutSystemdWithMountFlags(
	source, target, fstype string, options, sensitiveOptions, mountFlags []string,
) error {
	close(m.entered)
	<-m.release
	return m.FakeMounter.MountSensitiveWithoutSystemdWithMountFlags(source, target, fstype, options, sensitiveOptions, mountFlags)
}

func TestNodeTeardownCannotRacePublish(t *testing.T) {
	d, mounter := testTeardownDriver(t, true)
	release, cancel := context.WithCancel(t.Context())
	defer cancel()
	blocking := &teardownBlockingMounter{FakeMounter: mounter, entered: make(chan struct{}), release: release.Done()}
	d.mounter.Interface = blocking
	target := filepath.Join(d.teardown.podsDir, uuid.New(), "volumes/kubernetes.io~csi/pv/mount")
	published := make(chan error, 1)
	go func() {
		_, err := d.NodePublishVolume(t.Context(), &csi.NodePublishVolumeRequest{
			VolumeId:      "vol#lustrefs#1.1.1.1",
			TargetPath:    target,
			VolumeContext: map[string]string{"mgs-ip-address": "1.1.1.1", "fs-name": "lustrefs"},
			VolumeCapability: &csi.VolumeCapability{
				AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
				AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			},
		})
		published <- err
	}()
	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fake publish did not reach mount")
	}
	if d.teardown.mu.TryLock() {
		d.teardown.mu.Unlock()
		t.Fatal("publish must prevent transition into teardown until mount completes")
	}
	prepared := make(chan error, 1)
	go func() { prepared <- d.finishNodeTeardown(t.Context()) }()
	cancel()
	require.NoError(t, <-published)
	require.NoError(t, <-prepared)
	request, err := d.teardown.request()
	require.NoError(t, err)
	require.Empty(t, request, "the admitted publish must be counted before teardown can start")
}

func TestNodeTeardownTemporaryUnmountIsNotForced(t *testing.T) {
	d, mounter := testTeardownDriver(t, true)
	d.workingMountDir = t.TempDir()
	target := filepath.Join(d.workingMountDir, "target")
	require.NoError(t, os.Mkdir(target, 0o700))
	require.NoError(t, mounter.Mount("fake:/fs", target, "lustre", nil))
	require.NoError(t, d.internalUnmount("target"))
	require.NoDirExists(t, target)
}

func TestNodeTeardownWaitDoesNotHoldNodeLocks(t *testing.T) {
	d, _ := testTeardownDriver(t, true)
	d.teardown.timeout = 5 * time.Second
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := d.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{
			VolumeId: "last", TargetPath: filepath.Join(d.teardown.podsDir, "absent"),
		})
		done <- err
	}()
	require.Eventually(t, func() bool {
		request, err := d.teardown.request()
		return err == nil && request != ""
	}, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		if !d.teardown.mu.TryLock() {
			return false
		}
		d.teardown.mu.Unlock()
		return true
	}, time.Second, time.Millisecond)
	if !d.kernelModuleLock.TryLock() {
		t.Fatal("unpublish held the module lock while waiting for the loader")
	}
	d.kernelModuleLock.Unlock()
	select {
	case err := <-done:
		t.Fatalf("unpublish finished before cancellation or acknowledgement: %v", err)
	default:
	}
	cancel()
	require.Error(t, <-done)
}
