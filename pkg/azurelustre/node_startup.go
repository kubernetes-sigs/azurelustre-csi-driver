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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pborman/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	nodeStartupDirEnv = "AZURELUSTRE_NODE_STARTUP_DIR"
	nodeStartupMaxAge = time.Duration(nodeFactLeaseDuration) * time.Second
)

type nodeStartupRuntime struct {
	loadedClient      string
	policyFingerprint string
}

type nodeStartupObservation struct {
	runtime    nodeStartupRuntime
	observedAt time.Time
}

// nodeStartup separates a working CSI cleanup endpoint from permission to create
// mounts. Its directory is a Pod-private emptyDir shared only with the loader.
type nodeStartup struct {
	dir, identity string
	ready         atomic.Pointer[nodeStartupObservation]
	install       func(context.Context) error
	probe         func(context.Context) error
	observe       func() (nodeStartupRuntime, error)
	onReady       func(context.Context)
	retryDelay    time.Duration
	probeInterval time.Duration
}

func newNodeStartup(dir, desiredClient string) (*nodeStartup, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) == "/" {
		return nil, errors.New("node startup requires an absolute private directory")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("node startup path is not a directory")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	bootID, podUID := strings.TrimSpace(string(boot)), os.Getenv("POD_UID")
	if uuid.Parse(bootID) == nil || uuid.Parse(podUID) == nil || desiredClient == "" {
		return nil, errors.New("node startup requires boot, Pod and desired-client identities")
	}
	s := &nodeStartup{
		dir:           dir,
		identity:      bootID + " " + podUID + " " + desiredClient,
		retryDelay:    time.Minute,
		probeInterval: 5 * time.Second,
		install: func(ctx context.Context) error {
			cmd := exec.CommandContext(ctx, "/app/entrypoint.sh")
			cmd.Env = append(os.Environ(), "AZURELUSTRE_CSI_ROLE=driver-utils")
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			return runStartupCommand(cmd)
		},
		probe: func(ctx context.Context) error {
			return runStartupCommand(exec.CommandContext(ctx, "/app/readinessProbe.sh", "--runtime"))
		},
	}
	// A container restart must not inherit its predecessor's package success.
	if err := s.clearReady(); err != nil {
		return nil, err
	}
	return s, nil
}

// Cancel the owned process group, not just the shell: package tools and probe
// children must not keep running when their attempt or the CSI process stops.
func runStartupCommand(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

func (s *nodeStartup) clearReady() error {
	err := os.Remove(filepath.Join(s.dir, "driver-ready"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *nodeStartup) publishReady(observation *nodeStartupObservation) error {
	file, err := os.CreateTemp(s.dir, ".driver-ready-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			klog.ErrorS(err, "could not remove temporary node startup record")
		}
	}()
	_, writeErr := fmt.Fprintf(file, "%s %s %s %d\n", s.identity,
		observation.runtime.loadedClient, observation.runtime.policyFingerprint,
		observation.observedAt.Unix())
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(s.dir, "driver-ready"))
}

func (s *nodeStartup) run(ctx context.Context) {
	defer func() {
		s.ready.Store(nil)
		if err := s.clearReady(); err != nil {
			klog.ErrorS(err, "could not clear node startup readiness")
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		attempt, cancel := context.WithTimeout(ctx, 10*time.Minute)
		err := s.install(attempt)
		cancel()
		if err == nil {
			break
		}
		klog.ErrorS(err, "Lustre userspace setup failed; CSI cleanup remains available")
		if !waitForStartupPoll(ctx, s.retryDelay) {
			return
		}
	}
	// No package mutation occurs after successful installation. Subsequent
	// checks only observe compatible host-runtime health; new mounts also keep
	// their existing live MGS reachability and optional image-policy checks.
	for ctx.Err() == nil {
		before, err := s.observeRuntime()
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err == nil {
			err = s.probe(attempt)
		}
		cancel()
		if err == nil {
			after, observeErr := s.observeRuntime()
			err = observeErr
			if err == nil && before != after {
				err = errors.New("resident client or compatibility policy changed during runtime probe")
			}
		}
		var observation *nodeStartupObservation
		if err == nil && ctx.Err() == nil {
			observation = &nodeStartupObservation{runtime: before, observedAt: time.Now()}
			err = s.publishReady(observation)
		}
		if err != nil || ctx.Err() != nil {
			observation = nil
		}
		wasReady := s.ready.Swap(observation) != nil
		if observation == nil {
			if clearErr := s.clearReady(); clearErr != nil {
				klog.ErrorS(clearErr, "could not clear node startup readiness after failed probe")
			}
		}
		if err != nil && wasReady {
			klog.ErrorS(err, "Lustre runtime is not ready for new mounts; CSI cleanup remains available")
		}
		if s.isReady() && s.onReady != nil {
			s.onReady(ctx)
		}
		if !waitForStartupPoll(ctx, s.probeInterval) {
			return
		}
	}
}

func waitForStartupPoll(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *nodeStartup) isReady() bool {
	return s.currentObservation() != nil
}

func (s *nodeStartup) currentObservation() *nodeStartupObservation {
	observation := s.ready.Load()
	if observation == nil {
		return nil
	}
	age := time.Since(observation.observedAt)
	if age < 0 || age > nodeStartupMaxAge {
		return nil
	}
	current, err := s.observeRuntime()
	if err != nil || current != observation.runtime {
		return nil
	}
	return observation
}

func (s *nodeStartup) observeRuntime() (nodeStartupRuntime, error) {
	if s.observe == nil {
		return nodeStartupRuntime{}, errors.New("host runtime observer is unavailable")
	}
	return s.observe()
}

func (d *Driver) observeNodeStartupRuntime() (nodeStartupRuntime, error) {
	policy, err := d.readMountAdmissionPolicy()
	if err != nil {
		return nodeStartupRuntime{}, err
	}
	clientPolicy, ok := policy.Clients[d.nodeFlavor]
	if !ok {
		return nodeStartupRuntime{}, fmt.Errorf("no client compatibility policy for flavor %q", d.nodeFlavor)
	}
	loaded := d.readNodeFactFile("/sys/module/lustre/version")
	if loaded == "" || (loaded != desiredClientIdentity(clientPolicy.Desired.Version, clientPolicy.Desired.ShaSuffix) &&
		!clientIsCompatible(loaded, clientPolicy.Compatible)) {
		return nodeStartupRuntime{}, fmt.Errorf("resident client %q is not supported for flavor %q", loaded, d.nodeFlavor)
	}
	return nodeStartupRuntime{loadedClient: loaded, policyFingerprint: policy.fingerprint}, nil
}

func (d *Driver) startNodeStartup(ctx context.Context) func() {
	s := d.nodeStartup
	if s == nil {
		return func() {}
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	taintRemoved := false
	removeAfter := time.Now().Add(d.taintRemovalInitialDelay)
	s.onReady = func(ctx context.Context) {
		if taintRemoved || !d.removeNotReadyTaint || d.kubeClient == nil || time.Now().Before(removeAfter) || !s.isReady() {
			return
		}
		attempt, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if err := removeNotReadyTaintContext(attempt, d.kubeClient, d.NodeID, d.Name); err != nil {
			klog.ErrorS(err, "could not remove node taint after startup; will retry")
			return
		}
		taintRemoved = true
	}
	go func() {
		defer close(done)
		s.run(workerCtx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func nodeStartupEnabled(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, container := range pod.Spec.Containers {
		if container.Name == "azurelustre" {
			for _, env := range container.Env {
				if env.Name == nodeStartupDirEnv {
					return env.Value != "" || env.ValueFrom != nil
				}
			}
		}
	}
	return false
}

func (d *Driver) checkNodeStartup() error {
	if d.nodeStartup != nil && !d.nodeStartup.isReady() {
		return fmt.Errorf("lustre package/runtime setup is not ready on node %q; cleanup remains available; retry after setup recovers", d.NodeID)
	}
	return nil
}
