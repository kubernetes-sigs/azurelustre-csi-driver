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
	"net"
	"net/http"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
)

const (
	statusReconcileTimeout = 60 * time.Second
	statusShutdownTimeout  = 10 * time.Second
	statusHealthAddress    = ":29654"
)

var errStatusLeadershipLost = errors.New("lost Azure Lustre node status controller leadership")

func (d *Driver) startNodeStatusController(ctx context.Context) error {
	if d.podRole != statusControllerPod {
		return fmt.Errorf("node status controller cannot run in %s role", d.podRole)
	}
	if d.kubeClient == nil || d.dynamicClient == nil {
		return fmt.Errorf("node status controller requires Kubernetes clients")
	}
	if d.podName == "" || d.podNamespace == "" || d.compatibilityPolicyConfigMap == "" {
		return fmt.Errorf("node status controller requires pod identity and compatibility policy ConfigMap")
	}
	if d.statusControllerElectionNamespace == "" || d.statusControllerElectionNamespace == d.podNamespace {
		return fmt.Errorf("status controller requires an election namespace separate from node fact writers")
	}
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "tcp", statusHealthAddress)
	if err != nil {
		return fmt.Errorf("listen for status-controller health: %w", err)
	}
	return serveNodeStatusController(ctx, listener, leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta: metav1.ObjectMeta{Name: statusControllerLeaseName, Namespace: d.statusControllerElectionNamespace},
			Client:    d.kubeClient.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{
				Identity: d.podName,
			},
		},
		LeaseDuration: 30 * time.Second,
		RenewDeadline: 20 * time.Second,
		RetryPeriod:   5 * time.Second,
		Name:          statusControllerLeaseName,
	}, d.statusControllerInterval, d.reconcileNodeStatuses)
}

type statusControllerHealth struct {
	watchdog *leaderelection.HealthzAdaptor
	running  atomic.Bool
	leading  atomic.Bool
	progress atomic.Int64
	maxIdle  time.Duration
}

func (h *statusControllerHealth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.running.Load() {
		http.Error(w, "status controller is stopping", http.StatusServiceUnavailable)
		return
	}
	if err := h.watchdog.Check(r); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if h.leading.Load() && time.Since(time.Unix(0, h.progress.Load())) > h.maxIdle {
		http.Error(w, "status reconciliation is stalled", http.StatusServiceUnavailable)
		return
	}
	// Followers are ready to take over; readiness is deliberately not "am I leader?"
	w.WriteHeader(http.StatusOK)
}

//nolint:contextcheck // The elector's cancellation-aware leader context is delivered through the acquired channel.
func serveNodeStatusController(
	parent context.Context,
	listener net.Listener,
	config leaderelection.LeaderElectionConfig,
	interval time.Duration,
	reconcile func(context.Context) error,
) (result error) {
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			klog.Warningf("close status health listener: %v", err)
		}
	}()
	if interval <= 0 {
		interval = defaultStatusControllerInterval
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	health := &statusControllerHealth{
		watchdog: leaderelection.NewLeaderHealthzAdaptor(0),
		maxIdle:  statusReconcileTimeout + interval + 15*time.Second,
	}
	health.running.Store(true)
	acquired := make(chan context.Context, 1)
	// Do not release early: cancellation of the elector can race in-flight
	// status writes. A successor waits for the lease to expire instead.
	config.ReleaseOnCancel = false
	config.Callbacks = leaderelection.LeaderCallbacks{
		OnStartedLeading: func(leaderCtx context.Context) {
			select {
			case acquired <- leaderCtx:
			case <-leaderCtx.Done():
			}
		},
		OnStoppedLeading: func() {
			health.running.Store(false)
		},
		OnNewLeader: func(identity string) {
			klog.Infof("Azure Lustre node status controller leader is %s", identity)
		},
	}
	elector, err := leaderelection.NewLeaderElector(config)
	if err != nil {
		return fmt.Errorf("configure status-controller election: %w", err)
	}
	health.watchdog.SetLeaderElection(elector)
	mux := http.NewServeMux()
	mux.Handle("/healthz", health)
	mux.Handle("/readyz", health)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	electionDone := make(chan struct{})
	go func() {
		defer close(electionDone)
		elector.Run(ctx)
	}()
	var loopDone chan struct{}
	defer func(ctx context.Context) {
		health.running.Store(false)
		cancel()
		shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), statusShutdownTimeout)
		defer stop()
		if err := server.Shutdown(shutdownCtx); err != nil {
			result = errors.Join(result, fmt.Errorf("shut down status health server: %w", err), server.Close())
		}
		for _, done := range []chan struct{}{electionDone, loopDone} {
			if done == nil {
				continue
			}
			select {
			case <-done:
			case <-shutdownCtx.Done():
				result = errors.Join(result, fmt.Errorf("stop status controller: %w", shutdownCtx.Err()))
			}
		}
	}(ctx)
	for {
		select {
		case <-parent.Done():
			return nil
		case <-electionDone:
			if parent.Err() != nil {
				return nil
			}
			return errStatusLeadershipLost
		case err := <-serverDone:
			return fmt.Errorf("status-controller health server stopped: %w", err)
		case <-loopDone:
			if parent.Err() != nil {
				return nil
			}
			// The only normal loop exit is cancellation, which in a running
			// service means the elector lost leadership.
			return errStatusLeadershipLost
		case leaderCtx := <-acquired:
			if ctx.Err() != nil || leaderCtx.Err() != nil {
				continue
			}
			health.progress.Store(time.Now().UnixNano())
			health.leading.Store(true)
			loopDone = make(chan struct{})
			go func() {
				defer close(loopDone)
				runNodeStatusReconciliation(leaderCtx, interval, reconcile, health)
			}()
		}
	}
}

func runNodeStatusReconciliation(
	ctx context.Context,
	interval time.Duration,
	reconcile func(context.Context) error,
	health *statusControllerHealth,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		health.progress.Store(time.Now().UnixNano())
		reconcileCtx, cancel := context.WithTimeout(ctx, statusReconcileTimeout)
		err := reconcile(reconcileCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			klog.Errorf("failed to reconcile Azure Lustre node status: %v", err)
		}
		health.progress.Store(time.Now().UnixNano())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
