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
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

func TestStatusControllerHealth(t *testing.T) {
	health := &statusControllerHealth{watchdog: leaderelection.NewLeaderHealthzAdaptor(0), maxIdle: time.Minute}
	for _, test := range []struct {
		name    string
		running bool
		leading bool
		idle    time.Duration
		want    int
	}{
		{"not running", false, false, 0, http.StatusServiceUnavailable},
		{"follower", true, false, time.Hour, http.StatusOK},
		{"active leader", true, true, 0, http.StatusOK},
		{"stalled leader", true, true, time.Hour, http.StatusServiceUnavailable},
		{"stopping leader", false, true, 0, http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			health.running.Store(test.running)
			health.leading.Store(test.leading)
			health.progress.Store(time.Now().Add(-test.idle).UnixNano())
			for _, path := range []string{"/healthz", "/readyz"} {
				response := httptest.NewRecorder()
				health.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
				if response.Code != test.want {
					t.Fatalf("%s status = %d, want %d", path, response.Code, test.want)
				}
			}
		})
	}
}

func TestStatusControllerRejectsSharedElectionNamespace(t *testing.T) {
	for _, namespace := range []string{"", "driver"} {
		driver := &Driver{
			podRole: statusControllerPod, podNamespace: "driver", podName: "controller",
			compatibilityPolicyConfigMap: "policy", statusControllerElectionNamespace: namespace,
			kubeClient: fake.NewSimpleClientset(), dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		}

		if err := driver.startNodeStatusController(t.Context()); err == nil {
			t.Fatalf("accepted election namespace %q", namespace)
		}
	}
}

func TestStatusControllerRejectsIncompleteConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, message string
		change        func(*Driver)
	}{
		{"node role", "cannot run in node role", func(d *Driver) { d.podRole = nodePod }},
		{"typed client", "requires Kubernetes clients", func(d *Driver) { d.kubeClient = nil }},
		{"dynamic client", "requires Kubernetes clients", func(d *Driver) { d.dynamicClient = nil }},
		{"pod name", "requires pod identity", func(d *Driver) { d.podName = "" }},
		{"pod namespace", "requires pod identity", func(d *Driver) { d.podNamespace = "" }},
		{"policy name", "requires pod identity", func(d *Driver) { d.compatibilityPolicyConfigMap = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver := &Driver{
				podRole: statusControllerPod, podNamespace: "driver", podName: "controller",
				compatibilityPolicyConfigMap: "policy", statusControllerElectionNamespace: "election",
				kubeClient: fake.NewSimpleClientset(), dynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
			}
			test.change(driver)
			require.ErrorContains(t, driver.startNodeStatusController(t.Context()), test.message)
		})
	}
}

func TestStatusControllerRejectsInvalidElectionConfiguration(t *testing.T) {
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	err = serveNodeStatusController(t.Context(), listener, leaderelection.LeaderElectionConfig{}, 0,
		func(context.Context) error {
			t.Error("invalid election must not start reconciliation")
			return nil
		})
	require.ErrorContains(t, err, "configure status-controller election")
	_, err = listener.Accept()
	require.ErrorIs(t, err, net.ErrClosed)
}

func TestStatusReconciliationRetriesFailuresAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	health := &statusControllerHealth{}
	calls := 0
	var deadlines []time.Time
	runNodeStatusReconciliation(ctx, time.Millisecond, func(reconcileCtx context.Context) error {
		calls++
		deadline, ok := reconcileCtx.Deadline()
		assert.True(t, ok, "each sweep must be bounded")
		deadlines = append(deadlines, deadline)
		if calls == 2 {
			cancel()
		}
		return errors.New("transient reconciliation failure")
	}, health)
	assert.Equal(t, 2, calls, "one failure must not terminate the leader's reconciliation loop")
	assert.WithinDuration(t, time.Now(), time.Unix(0, health.progress.Load()), time.Second)
	for _, deadline := range deadlines {
		assert.WithinDuration(t, time.Now().Add(statusReconcileTimeout), deadline, time.Second)
	}
}

// These tests exercise the real client-go election loop against an in-memory
// CAS lock, not a Kubernetes API server. They are not live HA evidence.
func TestStatusControllerLeadershipLoss(t *testing.T) {
	state := &testElectionState{}
	lock := &testElectionLock{state: state, identity: "leader"}
	entered := make(chan struct{})
	exited := make(chan struct{})
	cancel, done, address := startTestStatusService(t, lock, func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(exited)
		return ctx.Err()
	})
	defer cancel()
	awaitStatusSignal(t, entered)
	assertStatusProbe(t, address, "/healthz")
	lock.fail.Store(true)
	if err := awaitStatusResult(t, done); !errors.Is(err, errStatusLeadershipLost) {
		t.Fatalf("leadership loss error = %v", err)
	}
	awaitStatusSignal(t, exited)
}

func TestStatusControllerFollowerTakeoverAndCancellation(t *testing.T) {
	state := &testElectionState{}
	leaderEntered := make(chan struct{})
	leaderExited := make(chan struct{})
	cancelLeader, leaderDone, _ := startTestStatusService(t,
		&testElectionLock{state: state, identity: "first"}, func(ctx context.Context) error {
			close(leaderEntered)
			<-ctx.Done()
			close(leaderExited)
			return ctx.Err()
		})
	defer cancelLeader()
	awaitStatusSignal(t, leaderEntered)
	followerEntered := make(chan struct{})
	followerExited := make(chan struct{})
	cancelFollower, followerDone, address := startTestStatusService(t,
		&testElectionLock{state: state, identity: "second"}, func(ctx context.Context) error {
			close(followerEntered)
			<-ctx.Done()
			close(followerExited)
			return ctx.Err()
		})
	defer cancelFollower()
	assertStatusProbe(t, address, "/healthz")
	assertStatusProbe(t, address, "/readyz")
	select {
	case <-followerEntered:
		t.Fatal("follower reconciled while the original leader held the lock")
	default:
	}
	cancelLeader()
	if err := awaitStatusResult(t, leaderDone); err != nil {
		t.Fatalf("graceful leader cancellation: %v", err)
	}
	awaitStatusSignal(t, leaderExited)
	// ReleaseOnCancel=false leaves the lock intact until its lease expires.
	state.mu.Lock()
	holder := state.record.HolderIdentity
	state.mu.Unlock()
	if holder != "first" {
		t.Fatalf("leader released the lock before expiry: %q", holder)
	}
	awaitStatusSignal(t, followerEntered)
	assertStatusProbe(t, address, "/readyz")
	cancelFollower()
	if err := awaitStatusResult(t, followerDone); err != nil {
		t.Fatalf("graceful successor cancellation: %v", err)
	}
	awaitStatusSignal(t, followerExited)
}

func TestStatusControllerFollowerCancellation(t *testing.T) {
	lock := &testElectionLock{state: &testElectionState{}, identity: "unreachable"}
	lock.fail.Store(true)
	cancel, done, address := startTestStatusService(t, lock, func(context.Context) error {
		t.Error("follower must not reconcile")
		return nil
	})
	assertStatusProbe(t, address, "/readyz")
	cancel()
	if err := awaitStatusResult(t, done); err != nil {
		t.Fatalf("graceful follower cancellation: %v", err)
	}
}

func TestStatusControllerHealthServerFailure(t *testing.T) {
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock := &testElectionLock{state: &testElectionState{}, identity: "failed-listener"}
	lock.fail.Store(true)
	err = serveNodeStatusController(ctx, listener, testStatusElectionConfig(lock), time.Minute, func(context.Context) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "health server stopped") {
		t.Fatalf("health server failure = %v", err)
	}
}

func TestStatusControllerWatchdogDetectsStalledRenewal(t *testing.T) {
	block := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(block) })
	lock := &blockedStatusElectionLock{
		Interface: &testElectionLock{state: &testElectionState{}, identity: "stalled"},
		block:     block,
	}
	entered := make(chan struct{})
	cancel, done, address := startTestStatusService(t, lock, func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	defer cancel()
	awaitStatusSignal(t, entered)
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	ctx, stop := context.WithTimeout(t.Context(), 4*time.Second)
	defer stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode == http.StatusServiceUnavailable {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("expired leader lease remained healthy")
		case <-ticker.C:
		}
	}
	unblock.Do(func() { close(block) })
	if err := awaitStatusResult(t, done); !errors.Is(err, errStatusLeadershipLost) {
		t.Fatalf("stalled renewal exit = %v", err)
	}
}

type blockedStatusElectionLock struct {
	resourcelock.Interface
	block <-chan struct{}
}

func (l *blockedStatusElectionLock) Update(ctx context.Context, record resourcelock.LeaderElectionRecord) error {
	// Model a stuck transport that cannot return promptly on cancellation.
	<-l.block
	return l.Interface.Update(ctx, record)
}

func startTestStatusService(t *testing.T, lock resourcelock.Interface, reconcile func(context.Context) error) (
	context.CancelFunc, <-chan error, string,
) {
	t.Helper()
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- serveNodeStatusController(ctx, listener, testStatusElectionConfig(lock), time.Minute, reconcile)
	}()
	return cancel, done, "http://" + listener.Addr().String()
}

func testStatusElectionConfig(lock resourcelock.Interface) leaderelection.LeaderElectionConfig {
	return leaderelection.LeaderElectionConfig{
		Lock: lock, LeaseDuration: time.Second, RenewDeadline: 300 * time.Millisecond, RetryPeriod: 50 * time.Millisecond,
	}
}

func awaitStatusSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for status-controller event")
	}
}

func awaitStatusResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for status-controller exit")
		return nil
	}
}

func assertStatusProbe(t *testing.T, address, path string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s probe: %v", path, err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s probe = %d", path, response.StatusCode)
	}
}

type testElectionState struct {
	mu      sync.Mutex
	record  resourcelock.LeaderElectionRecord
	version int
}

type testElectionLock struct {
	state    *testElectionState
	identity string
	version  int
	fail     atomic.Bool
}

func (l *testElectionLock) Get(ctx context.Context) (*resourcelock.LeaderElectionRecord, []byte, error) {
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if l.fail.Load() {
		return nil, nil, errors.New("injected election API failure")
	}
	if l.state.version == 0 {
		return nil, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "leases"}, "election")
	}
	l.version = l.state.version
	record := l.state.record
	raw, err := json.Marshal(record)
	return &record, raw, err
}

func (l *testElectionLock) Create(ctx context.Context, record resourcelock.LeaderElectionRecord) error {
	return l.write(ctx, record, true)
}

func (l *testElectionLock) Update(ctx context.Context, record resourcelock.LeaderElectionRecord) error {
	return l.write(ctx, record, false)
}

func (l *testElectionLock) write(ctx context.Context, record resourcelock.LeaderElectionRecord, create bool) error {
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if l.fail.Load() {
		return errors.New("injected election API failure")
	}
	if create && l.state.version != 0 {
		return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "leases"}, "election")
	}
	if l.version != l.state.version {
		return apierrors.NewConflict(schema.GroupResource{Resource: "leases"}, "election", errors.New("stale resource version"))
	}
	l.state.record = record
	l.state.version++
	l.version = l.state.version
	return nil
}

func (*testElectionLock) RecordEvent(string) {}
func (l *testElectionLock) Identity() string { return l.identity }
func (l *testElectionLock) Describe() string { return "test/" + l.identity }
